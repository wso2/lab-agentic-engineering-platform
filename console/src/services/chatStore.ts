/**
 * Per-project chat store backing the requirements chat panel.
 *
 * - Messages persist to localStorage under `asdlc.chat.v1.<orgId>.<projectId>`.
 * - Schema version is recorded on the blob; mismatched blobs are dropped
 *   silently on read (chat history is transient by design).
 * - Two event buses are exposed:
 *     • subscribeChatStore — chat list updated.
 *     • subscribeRequirementsPageEvent — file written by the agent
 *       (the requirements page subscribes to refresh its file map).
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type ToolName =
  | 'str_replace'
  | 'create_file'
  | 'delete_file'
  | 'wireframe_add_screen'
  | 'wireframe_add_edge'
  | 'wireframe_remove_screen'
  | 'domain_add_entity'
  | 'domain_add_attribute'
  | 'domain_add_relation'
  | 'domain_remove_entity'
  | 'read_file';

// Friendly verbs for tool-card titles. Keeps the raw `toolName` available
// for filters / debugging while presenting users the higher-level action.
const TOOL_LABELS: Record<ToolName, string> = {
  str_replace: 'Modified',
  create_file: 'Created',
  delete_file: 'Deleted',
  wireframe_add_screen: 'Added screen',
  wireframe_add_edge: 'Linked screens',
  wireframe_remove_screen: 'Removed screen',
  domain_add_entity: 'Added entity',
  domain_add_attribute: 'Added attribute',
  domain_add_relation: 'Added relation',
  domain_remove_entity: 'Removed entity',
  read_file: 'Read',
};

export function toolDisplayLabel(name: ToolName): string {
  return TOOL_LABELS[name] ?? name;
}

export type ChatMessage =
  | UserMessage
  | AssistantMessage
  | ToolMessage
  | ErrorMessage;

export interface UserMessage {
  id: string;
  role: 'user';
  content: string;
  timestamp: number;
  turnId?: string;
  // Status of the agent turn this user message kicked off. Drives the
  // visibility of "Undo this turn" on the user bubble.
  turnStatus?: 'in_flight' | 'completed' | 'undone' | 'failed';
}

export interface AssistantMessage {
  id: string;
  role: 'assistant';
  content: string; // streamed text-delta accumulated in place
  timestamp: number;
  turnId?: string;
}

export interface ToolMessage {
  id: string; // matches the agents-service tool-call id
  role: 'tool';
  content: '';
  timestamp: number;
  turnId?: string;
  toolName: ToolName;
  toolStatus: 'running' | 'done' | 'error';
  toolFilename: string;
  toolSummary?: string;
  toolDiffStats?: { added: number; removed: number };
  toolDiffPreview?: string;
  toolErrorText?: string;
  toolErrorCode?: string;
}

export interface ErrorMessage {
  id: string;
  role: 'error';
  content: string; // human-readable message
  timestamp: number;
  turnId?: string;
  errorCode?: string;
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

const SCHEMA_VERSION = 1;
const STORE_KEY_PREFIX = 'asdlc.chat.v1.';
const MAX_MESSAGES_PER_PROJECT = 200;

/**
 * Per-file entry in the session's modified-set. Carries the turn that
 * first touched the file so we can scope undo / accept correctly.
 */
export interface ModifiedFileEntry {
  filename: string;
  firstTurnId: string;
  lastTurnId: string;
  updatedAt: number;
}

/**
 * The session baseline is captured by the BFF on the first chat turn
 * (when `requestSessionBaseline: true`). It pins a snapshot ID against
 * which per-file Revert restores; Accept just clears the file from the
 * modified-set, and when the set empties the baseline is dropped.
 */
export interface SessionBaseline {
  snapshotId: string;
  capturedAt: number;
}

interface StoredBlob {
  schemaVersion: number;
  messages: ChatMessage[];
  sessionBaseline?: SessionBaseline;
  modifiedFiles?: Record<string, ModifiedFileEntry>;
  updatedAt: number;
}

function storageKey(orgId: string, projectId: string): string {
  return `${STORE_KEY_PREFIX}${orgId}.${projectId}`;
}

/**
 * In-memory + on-disk shape used by every reader / mutator below. The
 * cache holds this directly; we serialise it to a `StoredBlob` on every
 * write. Splitting the in-memory shape out of `StoredBlob` keeps the
 * disk encoding loosely versioned (we control `schemaVersion`) without
 * threading it through every accessor.
 */
interface ProjectChatState {
  messages: ChatMessage[];
  sessionBaseline?: SessionBaseline;
  modifiedFiles: Record<string, ModifiedFileEntry>;
}

function readBlob(orgId: string, projectId: string): ProjectChatState {
  if (typeof window === 'undefined') return { messages: [], modifiedFiles: {} };
  try {
    const raw = window.localStorage.getItem(storageKey(orgId, projectId));
    if (!raw) return { messages: [], modifiedFiles: {} };
    const parsed = JSON.parse(raw) as StoredBlob;
    if (parsed.schemaVersion !== SCHEMA_VERSION) return { messages: [], modifiedFiles: {} };
    if (!Array.isArray(parsed.messages)) return { messages: [], modifiedFiles: {} };
    return {
      messages: parsed.messages,
      sessionBaseline: parsed.sessionBaseline,
      modifiedFiles: parsed.modifiedFiles ?? {},
    };
  } catch {
    return { messages: [], modifiedFiles: {} };
  }
}

function writeBlob(orgId: string, projectId: string, state: ProjectChatState): void {
  if (typeof window === 'undefined') return;
  const trimmed = state.messages.slice(-MAX_MESSAGES_PER_PROJECT);
  const blob: StoredBlob = {
    schemaVersion: SCHEMA_VERSION,
    messages: trimmed,
    sessionBaseline: state.sessionBaseline,
    modifiedFiles: state.modifiedFiles,
    updatedAt: Date.now(),
  };
  try {
    window.localStorage.setItem(storageKey(orgId, projectId), JSON.stringify(blob));
  } catch {
    /* quota exceeded — chat history is transient, drop silently */
  }
}

// ---------------------------------------------------------------------------
// In-memory store
// ---------------------------------------------------------------------------

interface ProjectKey {
  orgId: string;
  projectId: string;
}

const cache = new Map<string, ProjectChatState>();
const chatListeners = new Set<() => void>();
const EMPTY: ChatMessage[] = [];
const EMPTY_MODIFIED: Record<string, ModifiedFileEntry> = {};

function cacheKey(orgId: string, projectId: string): string {
  return `${orgId}.${projectId}`;
}

function load(orgId: string, projectId: string): ProjectChatState {
  const key = cacheKey(orgId, projectId);
  const existing = cache.get(key);
  if (existing) return existing;
  const fromDisk = readBlob(orgId, projectId);
  cache.set(key, fromDisk);
  return fromDisk;
}

function mutate(
  orgId: string,
  projectId: string,
  fn: (state: ProjectChatState) => ProjectChatState,
): void {
  const key = cacheKey(orgId, projectId);
  const current = load(orgId, projectId);
  const next = fn(current);
  if (next === current) return;
  cache.set(key, next);
  writeBlob(orgId, projectId, next);
  chatListeners.forEach((fn) => fn());
}

function mutateMessages(
  orgId: string,
  projectId: string,
  fn: (messages: ChatMessage[]) => ChatMessage[],
): void {
  mutate(orgId, projectId, (state) => {
    const next = fn(state.messages);
    if (next === state.messages) return state;
    return { ...state, messages: next };
  });
}

export function getChatMessages(orgId: string, projectId: string): ChatMessage[] {
  if (!orgId || !projectId) return EMPTY;
  return load(orgId, projectId).messages;
}

/** Returns the session baseline snapshot id, if one has been captured. */
export function getSessionBaseline(orgId: string, projectId: string): SessionBaseline | undefined {
  if (!orgId || !projectId) return undefined;
  return load(orgId, projectId).sessionBaseline;
}

/** Returns the per-file modified set keyed by filename. */
export function getModifiedFiles(orgId: string, projectId: string): Record<string, ModifiedFileEntry> {
  if (!orgId || !projectId) return EMPTY_MODIFIED;
  return load(orgId, projectId).modifiedFiles;
}

export function setSessionBaseline(key: ProjectKey, snapshotId: string): void {
  mutate(key.orgId, key.projectId, (state) => {
    if (state.sessionBaseline?.snapshotId === snapshotId) return state;
    return { ...state, sessionBaseline: { snapshotId, capturedAt: Date.now() } };
  });
}

export function clearSessionBaseline(key: ProjectKey): void {
  mutate(key.orgId, key.projectId, (state) => {
    if (!state.sessionBaseline) return state;
    return { ...state, sessionBaseline: undefined };
  });
}

export function markFileModified(key: ProjectKey, filenames: string[], turnId: string | undefined): void {
  if (filenames.length === 0) return;
  mutate(key.orgId, key.projectId, (state) => {
    const next = { ...state.modifiedFiles };
    const tid = turnId ?? '';
    let changed = false;
    const ts = Date.now();
    for (const filename of filenames) {
      const existing = next[filename];
      if (existing) {
        if (existing.lastTurnId !== tid) {
          next[filename] = { ...existing, lastTurnId: tid, updatedAt: ts };
          changed = true;
        }
      } else {
        next[filename] = { filename, firstTurnId: tid, lastTurnId: tid, updatedAt: ts };
        changed = true;
      }
    }
    if (!changed) return state;
    return { ...state, modifiedFiles: next };
  });
}

export function removeModifiedFile(key: ProjectKey, filename: string): void {
  mutate(key.orgId, key.projectId, (state) => {
    if (!state.modifiedFiles[filename]) return state;
    const next = { ...state.modifiedFiles };
    delete next[filename];
    return { ...state, modifiedFiles: next };
  });
}

export function clearChatHistory(orgId: string, projectId: string): void {
  mutate(orgId, projectId, () => ({ messages: [], modifiedFiles: {}, sessionBaseline: undefined }));
}

export function subscribeChatStore(listener: () => void): () => void {
  chatListeners.add(listener);
  return () => chatListeners.delete(listener);
}

let idCounter = 0;
export function nextId(prefix = 'msg'): string {
  return `${prefix}-${Date.now()}-${++idCounter}`;
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

export function appendUserMessage(key: ProjectKey, content: string): UserMessage {
  const msg: UserMessage = {
    id: nextId('u'),
    role: 'user',
    content,
    timestamp: Date.now(),
    turnStatus: 'in_flight',
  };
  mutateMessages(key.orgId, key.projectId, (msgs) => [...msgs, msg]);
  return msg;
}

export function setUserTurnId(key: ProjectKey, userMsgId: string, turnId: string): void {
  mutateMessages(key.orgId, key.projectId, (msgs) =>
    msgs.map((m) =>
      m.id === userMsgId && m.role === 'user' ? { ...m, turnId } : m,
    ),
  );
}

export function setTurnStatus(
  key: ProjectKey,
  turnId: string,
  status: UserMessage['turnStatus'],
): void {
  mutateMessages(key.orgId, key.projectId, (msgs) =>
    msgs.map((m) =>
      m.role === 'user' && m.turnId === turnId ? { ...m, turnStatus: status } : m,
    ),
  );
}

export function appendAssistantText(key: ProjectKey, turnId: string | undefined, delta: string): void {
  mutateMessages(key.orgId, key.projectId, (msgs) => {
    const last = msgs[msgs.length - 1];
    if (last && last.role === 'assistant' && last.turnId === turnId) {
      const updated: AssistantMessage = { ...last, content: last.content + delta };
      return [...msgs.slice(0, -1), updated];
    }
    const fresh: AssistantMessage = {
      id: nextId('a'),
      role: 'assistant',
      content: delta,
      timestamp: Date.now(),
      turnId,
    };
    return [...msgs, fresh];
  });
}

export function upsertToolMessage(key: ProjectKey, msg: ToolMessage): void {
  mutateMessages(key.orgId, key.projectId, (msgs) => {
    const idx = msgs.findIndex((m) => m.id === msg.id);
    if (idx < 0) return [...msgs, msg];
    return msgs.map((m, i) => (i === idx ? msg : m));
  });
}

export function appendErrorMessage(
  key: ProjectKey,
  turnId: string | undefined,
  content: string,
  errorCode?: string,
): void {
  const msg: ErrorMessage = {
    id: nextId('e'),
    role: 'error',
    content,
    timestamp: Date.now(),
    turnId,
    errorCode,
  };
  mutateMessages(key.orgId, key.projectId, (msgs) => [...msgs, msg]);
}

export function markTurnUndone(key: ProjectKey, turnId: string): void {
  mutateMessages(key.orgId, key.projectId, (msgs) =>
    msgs.map((m) =>
      m.turnId === turnId && m.role === 'user'
        ? { ...m, turnStatus: 'undone' as const }
        : m,
    ),
  );
}

// ---------------------------------------------------------------------------
// Page event bus — chat → requirements page
// ---------------------------------------------------------------------------

export type RequirementsPageEvent =
  | {
      kind: 'fileWritten';
      filename: string;
      content?: string;
      siblings?: Record<string, string>;
    }
  | { kind: 'turnStarted'; turnId: string; orgId: string; projectId: string }
  | { kind: 'turnEnded'; turnId: string; orgId: string; projectId: string }
  | { kind: 'busyPathsChanged'; paths: Set<string>; orgId: string; projectId: string };

const pageEventListeners = new Set<(e: RequirementsPageEvent) => void>();

export function publishRequirementsPageEvent(event: RequirementsPageEvent): void {
  pageEventListeners.forEach((fn) => fn(event));
}

export function subscribeRequirementsPageEvent(
  listener: (e: RequirementsPageEvent) => void,
): () => void {
  pageEventListeners.add(listener);
  return () => pageEventListeners.delete(listener);
}

// ---------------------------------------------------------------------------
// Panel open/close + copilot request (UI layout uses these)
// ---------------------------------------------------------------------------

let chatPanelOpen = false;
const panelListeners = new Set<() => void>();

export function isChatPanelOpen(): boolean {
  return chatPanelOpen;
}

export function setChatPanelOpen(open: boolean): void {
  chatPanelOpen = open;
  panelListeners.forEach((fn) => fn());
}

const copilotRequestListeners = new Set<() => void>();

export function requestCopilotOpen(): void {
  copilotRequestListeners.forEach((fn) => fn());
}

export function subscribeCopilotRequest(listener: () => void): () => void {
  copilotRequestListeners.add(listener);
  return () => copilotRequestListeners.delete(listener);
}

export function subscribeChatPanelState(listener: () => void): () => void {
  panelListeners.add(listener);
  return () => panelListeners.delete(listener);
}

// ---------------------------------------------------------------------------
// Conversation history adapter
// ---------------------------------------------------------------------------

/** Build the chat history payload the BFF expects (user + assistant only). */
export function asChatHistory(
  messages: ChatMessage[],
): { role: 'user' | 'assistant'; content: string }[] {
  const out: { role: 'user' | 'assistant'; content: string }[] = [];
  for (const m of messages) {
    if (m.role === 'user') out.push({ role: 'user', content: m.content });
    else if (m.role === 'assistant' && m.content.trim() !== '')
      out.push({ role: 'assistant', content: m.content });
  }
  return out;
}
