import { useEffect, useRef, useState } from 'react';
import * as Y from 'yjs';
import { WebsocketProvider } from 'y-websocket';
import * as encoding from 'lib0/encoding';
import * as decoding from 'lib0/decoding';
import { getToken } from '../services/api/rest';

export interface CollabPeer {
  clientId: number;
  name: string;
  color: string;
  editing: boolean;
}

export interface UseCollabEditorResult {
  connected: boolean;
  peers: CollabPeer[];
  ydoc: Y.Doc | null;
  provider: WebsocketProvider | null;
  user: { name: string; color: string } | null;
}

const PEER_COLORS = [
  '#e57373', '#64b5f6', '#81c784', '#ffb74d',
  '#ba68c8', '#4dd0e1', '#f06292', '#aed581',
];

function pickColor(clientId: number): string {
  return PEER_COLORS[clientId % PEER_COLORS.length];
}

const MSG_SEED = 50;

export interface UseCollabEditorOptions {
  roomId: string | null;
  orgId?: string;
  projectId?: string;
  userName?: string;
  getMarkdown?: () => string;
  onSave?: (markdown: string) => void;
  onSeedRequested?: (markdown: string) => void;
  isEditing?: boolean;
  /**
   * When true, the 5s auto-save tick early-returns. Used by the requirements
   * chat flow: while a chat turn is in flight, the BFF holds an advisory
   * lock on the requirements dir, so this loop would just get 409s. Pausing
   * also avoids overwriting the BFF-authoritative content the page is
   * reseeding via `editorRef.current?.setActiveMarkdown(...)` after each
   * tool result. See docs/design/requirements-chat.md §4.5.
   */
  paused?: boolean;
}

export function useCollabEditor(opts: UseCollabEditorOptions): UseCollabEditorResult {
  const {
    roomId, orgId, projectId, userName,
    getMarkdown, onSave, onSeedRequested, isEditing,
    paused,
  } = opts;

  const [connected, setConnected] = useState(false);
  const [peers, setPeers] = useState<CollabPeer[]>([]);
  const [ydoc, setYdoc] = useState<Y.Doc | null>(null);
  const [provider, setProvider] = useState<WebsocketProvider | null>(null);
  const [user, setUser] = useState<{ name: string; color: string } | null>(null);

  const cleanedUpRef = useRef(false);
  const userNameRef = useRef(userName);
  userNameRef.current = userName;
  const getMarkdownRef = useRef(getMarkdown);
  getMarkdownRef.current = getMarkdown;
  const onSaveRef = useRef(onSave);
  onSaveRef.current = onSave;
  const onSeedRequestedRef = useRef(onSeedRequested);
  onSeedRequestedRef.current = onSeedRequested;
  const pausedRef = useRef(!!paused);
  pausedRef.current = !!paused;

  useEffect(() => {
    if (!roomId) return;
    if (!userName) return;
    cleanedUpRef.current = false;

    let saveTimer: ReturnType<typeof setInterval> | null = null;
    let yDocLocal: Y.Doc | null = null;
    let providerLocal: WebsocketProvider | null = null;

    const start = async () => {
      const token = await getToken();
      const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${wsProto}//${window.location.host}/collab/${roomId}`;

      const yDocInst = new Y.Doc();
      yDocLocal = yDocInst;

      const providerInst = new WebsocketProvider(wsUrl, roomId, yDocInst, {
        params: {
          token,
          org: orgId ?? '',
          project: projectId ?? '',
        },
      });
      providerLocal = providerInst;

      providerInst.messageHandlers[MSG_SEED] = (
        _enc: encoding.Encoder,
        decoder: decoding.Decoder,
      ) => {
        try {
          const markdown = decoding.readVarString(decoder);
          if (cleanedUpRef.current) return;
          onSeedRequestedRef.current?.(markdown);
        } catch (err) {
          console.error('[collab] failed to handle seed message:', err);
        }
      };

      const localUser = {
        name: userNameRef.current ?? 'User',
        color: pickColor(yDocInst.clientID),
      };
      setUser(localUser);
      setYdoc(yDocInst);
      setProvider(providerInst);

      providerInst.on('sync', (synced: boolean) => {
        if (!synced || cleanedUpRef.current) return;
        setConnected(true);
        if (!saveTimer) {
          saveTimer = setInterval(() => {
            if (cleanedUpRef.current) return;
            // Skip auto-save while a chat turn is mutating the working tree
            // — the BFF holds an advisory lock for the duration and would
            // 409 us anyway, but suppressing the call avoids the noise.
            if (pausedRef.current) return;
            const md = getMarkdownRef.current?.();
            if (md != null) onSaveRef.current?.(md);
          }, 5000);
        }
      });

      providerInst.awareness.on('change', () => {
        if (cleanedUpRef.current) return;
        const states = providerInst.awareness.getStates();
        const list: CollabPeer[] = [];
        states.forEach((state, clientId) => {
          if (clientId === yDocInst.clientID) return;
          const u = (state as { user?: { name?: string; editing?: boolean } } | undefined)?.user;
          if (!u) return;
          list.push({
            clientId,
            name: u.name ?? 'Unknown',
            color: pickColor(clientId),
            editing: !!u.editing,
          });
        });
        setPeers(list);
      });

      providerInst.awareness.setLocalStateField('user', { ...localUser, editing: false });
    };

    start();

    return () => {
      cleanedUpRef.current = true;
      if (saveTimer) clearInterval(saveTimer);
      if (providerLocal) providerLocal.destroy();
      if (yDocLocal) yDocLocal.destroy();
      setPeers([]);
      setConnected(false);
      setYdoc(null);
      setProvider(null);
      setUser(null);
    };
  }, [roomId, orgId, projectId, userName]);

  useEffect(() => {
    if (!provider) return;
    const current = provider.awareness.getLocalState() as
      | { user?: Record<string, unknown> }
      | null;
    provider.awareness.setLocalStateField('user', {
      ...(current?.user ?? {}),
      editing: !!isEditing,
    });
  }, [provider, isEditing]);

  useEffect(() => {
    if (!provider || !userName) return;
    const current = provider.awareness.getLocalState() as
      | { user?: Record<string, unknown> }
      | null;
    provider.awareness.setLocalStateField('user', {
      ...(current?.user ?? {}),
      name: userName,
    });
    setUser((prev) => (prev ? { ...prev, name: userName } : prev));
  }, [provider, userName]);

  return { connected, peers, ydoc, provider, user };
}
