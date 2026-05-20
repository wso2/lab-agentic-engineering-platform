import * as http from 'node:http';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { WebSocketServer, WebSocket } from 'ws';
import * as Y from 'yjs';
import * as syncProtocol from 'y-protocols/sync';
import * as awarenessProtocol from 'y-protocols/awareness';
import * as encoding from 'lib0/encoding';
import * as decoding from 'lib0/decoding';

const MSG_SYNC = 0;
const MSG_AWARENESS = 1;
const MSG_SEED = 50;

const PM_FRAGMENT_FIELD = 'default';

const BFF_URL = process.env.BFF_URL ?? 'http://localhost:9090';
const PORT = Number(process.env.PORT ?? 3400);
const STATE_DIR = process.env.STATE_DIR ?? './data/collab-state';
const PERSIST_DEBOUNCE_MS = 1000;

fs.mkdirSync(STATE_DIR, { recursive: true });

interface Room {
  doc: Y.Doc;
  awareness: awarenessProtocol.Awareness;
  peers: Map<WebSocket, string>; // ws → userId
  cleanupTimer: ReturnType<typeof setTimeout> | null;
  persistTimer: ReturnType<typeof setTimeout> | null;
  contentInitialized: boolean;
  seedingPeer: WebSocket | null;
}

const rooms = new Map<string, Room>();

// ── Auth ──────────────────────────────────────────────────────────────────────

interface UserInfo {
  userId: string;
  name: string;
  email: string;
}

async function validateToken(token: string, roomId: string): Promise<UserInfo | null> {
  try {
    const res = await fetch(`${BFF_URL}/api/v1/collab/validate`, {
      headers: {
        Authorization: `Bearer ${token}`,
        'X-Room-Id': roomId,
      },
    });
    if (!res.ok) return null;
    const data = await res.json() as { name: string; email: string };
    return { userId: data.email || data.name, name: data.name, email: data.email };
  } catch {
    return null;
  }
}

// ── Room helpers ──────────────────────────────────────────────────────────────

function statePath(roomId: string): string {
  const safe = roomId.replace(/[^a-zA-Z0-9._-]/g, '_');
  return path.join(STATE_DIR, `${safe}.bin`);
}

function loadPersistedState(roomId: string): Uint8Array | null {
  try {
    const buf = fs.readFileSync(statePath(roomId));
    return new Uint8Array(buf);
  } catch {
    return null;
  }
}

function persistRoomState(roomId: string, doc: Y.Doc): void {
  try {
    const update = Y.encodeStateAsUpdate(doc);
    fs.writeFileSync(statePath(roomId), Buffer.from(update));
  } catch (err) {
    console.error(`[collab] failed to persist state for ${roomId}:`, err);
  }
}

function schedulePersist(room: Room, roomId: string): void {
  if (room.persistTimer) return;
  room.persistTimer = setTimeout(() => {
    room.persistTimer = null;
    persistRoomState(roomId, room.doc);
  }, PERSIST_DEBOUNCE_MS);
}

function fragmentHasContent(doc: Y.Doc): boolean {
  return doc.getXmlFragment(PM_FRAGMENT_FIELD).length > 0;
}

function getOrCreateRoom(roomId: string): Room {
  if (!rooms.has(roomId)) {
    const doc = new Y.Doc();
    const awareness = new awarenessProtocol.Awareness(doc);
    let contentInitialized = false;

    const persisted = loadPersistedState(roomId);
    if (persisted && persisted.length > 0) {
      try {
        Y.applyUpdate(doc, persisted);
        contentInitialized = fragmentHasContent(doc);
      } catch (err) {
        console.error(`[collab] failed to apply persisted state for ${roomId}:`, err);
      }
    }

    const room: Room = {
      doc,
      awareness,
      peers: new Map(),
      cleanupTimer: null,
      persistTimer: null,
      contentInitialized,
      seedingPeer: null,
    };

    doc.on('update', () => {
      if (!room.contentInitialized && fragmentHasContent(doc)) {
        room.contentInitialized = true;
        room.seedingPeer = null;
      }
      schedulePersist(room, roomId);
    });

    rooms.set(roomId, room);
  }
  return rooms.get(roomId)!;
}

async function fetchSpecContent(token: string, org: string, project: string): Promise<string | null> {
  try {
    const res = await fetch(`${BFF_URL}/api/v1/organizations/${encodeURIComponent(org)}/projects/${encodeURIComponent(project)}/spec`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) return null;
    const data = await res.json() as { content?: string } | null;
    return data?.content ?? '';
  } catch {
    return null;
  }
}

function sendSeedRequest(ws: WebSocket, markdown: string): void {
  const enc = encoding.createEncoder();
  encoding.writeVarUint(enc, MSG_SEED);
  encoding.writeVarString(enc, markdown);
  ws.send(encoding.toUint8Array(enc));
}

async function maybeRequestSeed(
  room: Room,
  ws: WebSocket,
  token: string,
  org: string | null,
  project: string | null,
): Promise<void> {
  if (room.contentInitialized) return;
  if (room.seedingPeer) return;
  if (!org || !project) return;

  const markdown = await fetchSpecContent(token, org, project);
  if (markdown == null || markdown.length === 0) return;
  if (room.contentInitialized) return;

  room.seedingPeer = ws;
  sendSeedRequest(ws, markdown);
}

function sendYjsSync(ws: WebSocket, room: Room): void {
  const enc1 = encoding.createEncoder();
  encoding.writeVarUint(enc1, MSG_SYNC);
  syncProtocol.writeSyncStep1(enc1, room.doc);
  ws.send(encoding.toUint8Array(enc1));

  const enc2 = encoding.createEncoder();
  encoding.writeVarUint(enc2, MSG_SYNC);
  syncProtocol.writeSyncStep2(enc2, room.doc, new Uint8Array([0]));
  ws.send(encoding.toUint8Array(enc2));

  if (room.awareness.getStates().size > 0) {
    const enc3 = encoding.createEncoder();
    encoding.writeVarUint(enc3, MSG_AWARENESS);
    const awarenessUpdate = awarenessProtocol.encodeAwarenessUpdate(
      room.awareness, [...room.awareness.getStates().keys()],
    );
    encoding.writeVarUint8Array(enc3, awarenessUpdate);
    ws.send(encoding.toUint8Array(enc3));
  }
}

function broadcast(room: Room, sender: WebSocket, msg: Buffer): void {
  for (const peer of room.peers.keys()) {
    if (peer !== sender && peer.readyState === WebSocket.OPEN) peer.send(msg);
  }
}

function onPeerDisconnect(room: Room, roomId: string, ws: WebSocket): void {
  room.peers.delete(ws);
  if (room.seedingPeer === ws && !room.contentInitialized) {
    room.seedingPeer = null;
  }
  if (room.peers.size === 0) {
    room.cleanupTimer = setTimeout(() => {
      if (room.persistTimer) {
        clearTimeout(room.persistTimer);
        room.persistTimer = null;
      }
      persistRoomState(roomId, room.doc);
      room.doc.destroy();
      rooms.delete(roomId);
      console.log(`[collab] room expired: ${roomId}`);
    }, 30_000);
  }
}

// ── HTTP server (health check only) ──────────────────────────────────────────

const server = http.createServer((req, res) => {
  if (req.url === '/collab/api/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ status: 'ok' }));
    return;
  }
  res.writeHead(404);
  res.end('not found');
});

// ── WebSocket server ──────────────────────────────────────────────────────────

const wss = new WebSocketServer({ server });

wss.on('connection', async (ws, req) => {
  const url = new URL(req.url ?? '/', 'http://localhost');
  const pathParts = url.pathname.split('/').filter(Boolean);
  const roomId = pathParts[1] ?? '';
  const token = url.searchParams.get('token') ?? '';
  const org = url.searchParams.get('org');
  const project = url.searchParams.get('project');

  if (!roomId || !token || pathParts[0] !== 'collab') {
    ws.close(1008, 'missing roomId or token');
    return;
  }

  const user = await validateToken(token, roomId);
  if (!user) {
    ws.close(1008, 'unauthorized');
    return;
  }

  const room = getOrCreateRoom(roomId);
  if (room.cleanupTimer) {
    clearTimeout(room.cleanupTimer);
    room.cleanupTimer = null;
  }

  room.peers.set(ws, user.userId);
  console.log(`[collab] ${user.name} joined room ${roomId} (${room.peers.size} peers)`);

  // Track which Y.js clientIDs this connection owns so we can clean up on disconnect
  const connClientIds = new Set<number>();
  const onAwarenessChange = ({ added, updated }: { added: number[]; updated: number[] }, origin: unknown) => {
    if (origin !== ws) return;
    for (const id of [...added, ...updated]) connClientIds.add(id);
  };
  room.awareness.on('change', onAwarenessChange);

  // Register message handler BEFORE sending the server's sync so we don't miss
  // the client's initial step1 that arrives during the async seed/auth awaits.
  ws.on('message', (rawMsg) => {
    const msg = rawMsg as Buffer;
    try {
      const decoder = decoding.createDecoder(new Uint8Array(msg));
      const msgType = decoding.readVarUint(decoder);

      if (msgType === MSG_SYNC) {
        const replyEnc = encoding.createEncoder();
        encoding.writeVarUint(replyEnc, MSG_SYNC);
        syncProtocol.readSyncMessage(decoder, replyEnc, room.doc, null);
        if (encoding.length(replyEnc) > 1) ws.send(encoding.toUint8Array(replyEnc));
        broadcast(room, ws, msg);
      } else if (msgType === MSG_AWARENESS) {
        awarenessProtocol.applyAwarenessUpdate(
          room.awareness, decoding.readVarUint8Array(decoder), ws,
        );
        broadcast(room, ws, msg);
      }
    } catch (err) {
      console.error('[collab] message error:', err);
    }
  });

  sendYjsSync(ws, room);

  void maybeRequestSeed(room, ws, token, org, project);

  ws.on('close', () => {
    console.log(`[collab] ${user.name} disconnected from room ${roomId}`);
    room.awareness.off('change', onAwarenessChange);
    if (connClientIds.size > 0) {
      // Immediately remove this peer's awareness states so others don't see a ghost
      awarenessProtocol.removeAwarenessStates(room.awareness, [...connClientIds], null);
      const enc = encoding.createEncoder();
      encoding.writeVarUint(enc, MSG_AWARENESS);
      encoding.writeVarUint8Array(enc, awarenessProtocol.encodeAwarenessUpdate(room.awareness, [...connClientIds]));
      broadcast(room, ws, Buffer.from(encoding.toUint8Array(enc)));
    }
    onPeerDisconnect(room, roomId, ws);
  });

  ws.on('error', (err) => {
    console.error(`[collab] ws error for ${user.name}:`, err);
  });
});

server.listen(PORT, () => {
  console.log(`[collab] collab-server listening on port ${PORT}`);
});
