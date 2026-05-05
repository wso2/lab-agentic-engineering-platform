/**
 * MOCK: In-memory chat message store.
 *
 * Persists chat messages across navigation within the same browser session.
 * Messages are scoped per project so each project has its own chat history.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface ChatMessage {
  id: string;
  role: 'user' | 'assistant' | 'tool';
  content: string;
  /** For tool messages: the name of the tool that was called */
  toolName?: string;
  /** For tool messages: brief summary of what was changed */
  toolSummary?: string;
  /** For tool messages: status of the tool call */
  toolStatus?: 'running' | 'done';
  timestamp: number;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

const chatStore = new Map<string, ChatMessage[]>();
const listeners = new Set<() => void>();
const EMPTY: ChatMessage[] = [];

function key(projectId: string): string {
  return projectId || '__global__';
}

export function getChatMessages(projectId: string): ChatMessage[] {
  return chatStore.get(key(projectId)) ?? EMPTY;
}

export function addChatMessage(projectId: string, message: ChatMessage): void {
  const k = key(projectId);
  const existing = chatStore.get(k) ?? [];
  chatStore.set(k, [...existing, message]);
  listeners.forEach((fn) => fn());
}

export function updateChatMessage(projectId: string, messageId: string, updates: Partial<ChatMessage>): void {
  const k = key(projectId);
  const existing = chatStore.get(k) ?? [];
  chatStore.set(
    k,
    existing.map((m) => (m.id === messageId ? { ...m, ...updates } : m)),
  );
  listeners.forEach((fn) => fn());
}

export function subscribeChatStore(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// ---------------------------------------------------------------------------
// MOCK: Page update event bus
// ---------------------------------------------------------------------------

export type PageUpdatePayload = {
  tab: string;
  action: string;
  data?: string;
};

const pageUpdateListeners = new Set<(payload: PageUpdatePayload) => void>();

export function emitPageUpdate(payload: PageUpdatePayload): void {
  pageUpdateListeners.forEach((fn) => fn(payload));
}

export function onPageUpdate(listener: (payload: PageUpdatePayload) => void): () => void {
  pageUpdateListeners.add(listener);
  return () => pageUpdateListeners.delete(listener);
}

// ---------------------------------------------------------------------------
// MOCK: Chat panel open/close state (shared with layout)
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

/**
 * MOCK: Request the layout to open the copilot panel.
 * Pages call this; the layout listens via subscribeCopilotRequest.
 */
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
// Helpers
// ---------------------------------------------------------------------------

let idCounter = 0;
export function nextMessageId(): string {
  return `msg-${Date.now()}-${++idCounter}`;
}
