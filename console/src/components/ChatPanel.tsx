/**
 * Requirements-chat panel. Replaces the v0 hardcoded mock with a real SSE
 * consumer that streams turns from the BFF (POST /requirements/chat).
 *
 * Responsibilities:
 *   - Send chat turn + render assistant text deltas + tool cards.
 *   - Persist history to localStorage via chatStore (per project).
 *   - Publish file-write events to ProjectRequirementsPage via the page
 *     event bus so the editor refreshes from the BFF-authoritative
 *     content on each tool result.
 *   - Surface per-turn Undo on the user bubble.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useParams } from 'react-router-dom';
import {
  Box,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  Stack,
  TextField,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import {
  ArrowUp,
  Check,
  Loader2,
  RotateCcw,
  Sparkles,
  Wrench,
  X,
} from '@wso2/oxygen-ui-icons-react';
import {
  type ChatMessage,
  type ToolMessage,
  type UserMessage,
  appendAssistantText,
  appendErrorMessage,
  appendUserMessage,
  asChatHistory,
  getChatMessages,
  getSessionBaseline,
  markFileModified,
  markTurnUndone,
  publishRequirementsPageEvent,
  setChatPanelOpen,
  setSessionBaseline,
  setTurnStatus,
  setUserTurnId,
  subscribeChatStore,
  toolDisplayLabel,
  upsertToolMessage,
} from '../services/chatStore';
import { api } from '../services/api';

// ---------------------------------------------------------------------------
// Tab routing — chat is wired for the requirements tab only in v1.
// ---------------------------------------------------------------------------

function resolveTabLabel(pathname: string): string {
  if (pathname.includes('/requirements')) return 'Requirements';
  if (pathname.includes('/architecture')) return 'Architecture';
  if (pathname.includes('/implementation-plan')) return 'Implementation Plan';
  if (pathname.includes('/components/')) return 'Component';
  if (pathname.includes('/components')) return 'Components';
  return 'Overview';
}

function resolveTabKey(pathname: string): string {
  if (pathname.includes('/requirements')) return 'requirements';
  if (pathname.includes('/architecture')) return 'architecture';
  if (pathname.includes('/implementation-plan')) return 'implementation-plan';
  if (pathname.includes('/components')) return 'components';
  return 'overview';
}

// ---------------------------------------------------------------------------
// Message bubbles
// ---------------------------------------------------------------------------

function UserBubble({
  message,
  onUndo,
  canUndo,
  isUndoing,
}: {
  message: UserMessage;
  onUndo: () => void;
  canUndo: boolean;
  isUndoing: boolean;
}) {
  const theme = useTheme();
  return (
    <Stack alignItems="flex-end" sx={{ mb: 1.5 }} gap={0.5}>
      <Box
        sx={{
          maxWidth: '85%',
          px: 2,
          py: 1.25,
          borderRadius: '16px 16px 4px 16px',
          bgcolor: theme.vars?.palette.primary.main ?? 'primary.main',
          color: theme.vars?.palette.primary.contrastText ?? '#fff',
          textDecoration: message.turnStatus === 'undone' ? 'line-through' : undefined,
          opacity: message.turnStatus === 'undone' ? 0.65 : 1,
        }}
      >
        <Typography variant="body2" sx={{ lineHeight: 1.6, fontSize: '0.8125rem' }}>
          {message.content}
        </Typography>
      </Box>
      {canUndo && (
        <Chip
          size="small"
          icon={isUndoing ? <CircularProgress size={10} /> : <RotateCcw size={12} />}
          label={isUndoing ? 'Undoing...' : 'Undo this turn'}
          onClick={isUndoing ? undefined : onUndo}
          sx={{
            height: 22,
            fontSize: '0.7rem',
            cursor: isUndoing ? 'default' : 'pointer',
          }}
          data-testid="undo-turn"
        />
      )}
    </Stack>
  );
}

function renderMarkdownLite(text: string): React.ReactNode {
  const parts = text.split(/(\*\*[^*]+\*\*)/g);
  return parts.map((part, i) => {
    if (part.startsWith('**') && part.endsWith('**')) {
      return <strong key={i}>{part.slice(2, -2)}</strong>;
    }
    return part;
  });
}

function AssistantBubble({ content }: { content: string }) {
  const theme = useTheme();
  return (
    <Stack direction="row" alignItems="flex-start" gap={1} sx={{ mb: 1.5 }}>
      <Box
        sx={{
          width: 28,
          height: 28,
          borderRadius: '50%',
          bgcolor: theme.vars?.palette.action?.selected ?? 'action.selected',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          flexShrink: 0,
          mt: 0.25,
        }}
      >
        <Sparkles size={14} />
      </Box>
      <Box
        sx={{
          maxWidth: '85%',
          px: 2,
          py: 1.25,
          borderRadius: '16px 16px 16px 4px',
          bgcolor: theme.vars?.palette.action?.hover ?? 'action.hover',
        }}
      >
        <Typography variant="body2" sx={{ lineHeight: 1.6, fontSize: '0.8125rem', whiteSpace: 'pre-wrap' }}>
          {renderMarkdownLite(content)}
        </Typography>
      </Box>
    </Stack>
  );
}

function ToolCard({ msg }: { msg: ToolMessage }) {
  const theme = useTheme();
  const [open, setOpen] = useState(false);
  const isRunning = msg.toolStatus === 'running';
  const isError = msg.toolStatus === 'error';
  const borderColor = isError
    ? theme.vars?.palette.error?.main ?? 'error.main'
    : isRunning
      ? theme.vars?.palette.warning?.main ?? 'warning.main'
      : theme.vars?.palette.success?.main ?? 'success.main';
  const bg = isError
    ? 'rgba(244, 67, 54, 0.06)'
    : isRunning
      ? 'rgba(255, 152, 0, 0.06)'
      : 'rgba(76, 175, 80, 0.06)';

  return (
    <Stack
      direction="row"
      alignItems="flex-start"
      gap={1}
      sx={{ mb: 1.5 }}
      data-testid="tool-card"
      data-tool-name={msg.toolName}
      data-tool-filename={msg.toolFilename}
      data-tool-status={msg.toolStatus}
    >
      <Box sx={{ width: 28, flexShrink: 0 }} />
      <Box
        sx={{
          width: '100%',
          border: '1px solid',
          borderColor,
          borderRadius: 2,
          overflow: 'hidden',
        }}
      >
        <Stack
          direction="row"
          alignItems="center"
          gap={1}
          sx={{
            px: 1.5,
            py: 1,
            bgcolor: bg,
            cursor: msg.toolDiffPreview ? 'pointer' : 'default',
          }}
          onClick={() => msg.toolDiffPreview && setOpen((v) => !v)}
        >
          {isRunning ? (
            <Loader2 size={14} style={{ animation: 'spin 1s linear infinite' }} />
          ) : isError ? (
            <X size={14} color={theme.vars?.palette.error?.main as string ?? '#e53935'} />
          ) : (
            <Check size={14} color={theme.vars?.palette.success?.main as string ?? '#4caf50'} />
          )}
          <Wrench size={12} style={{ opacity: 0.5 }} />
          <Typography variant="caption" fontWeight={600} sx={{ fontSize: '0.75rem' }}>
            {toolDisplayLabel(msg.toolName)}
          </Typography>
          <Chip
            label={msg.toolFilename}
            size="small"
            sx={{ height: 18, fontSize: '0.65rem', '& .MuiChip-label': { px: 0.5 } }}
          />
          {msg.toolDiffStats && (
            <Typography variant="caption" sx={{ fontSize: '0.7rem', color: 'text.secondary' }}>
              +{msg.toolDiffStats.added}/-{msg.toolDiffStats.removed}
            </Typography>
          )}
        </Stack>
        {msg.toolSummary && (
          <Box sx={{ px: 1.5, py: 0.75, borderTop: '1px solid', borderColor: 'divider' }}>
            <Typography variant="caption" color="text.secondary" sx={{ fontSize: '0.7rem' }}>
              {msg.toolSummary}
            </Typography>
          </Box>
        )}
        {open && msg.toolDiffPreview && (
          <Box sx={{ px: 1.5, py: 0.75, borderTop: '1px solid', borderColor: 'divider', bgcolor: 'background.default' }}>
            <Box
              component="pre"
              sx={{
                m: 0,
                fontSize: '0.7rem',
                fontFamily: 'monospace',
                whiteSpace: 'pre-wrap',
                lineHeight: 1.4,
              }}
            >
              {msg.toolDiffPreview}
            </Box>
          </Box>
        )}
        {isError && msg.toolErrorText && (
          <Box sx={{ px: 1.5, py: 0.75, borderTop: '1px solid', borderColor: 'divider' }}>
            <Typography variant="caption" color="error" sx={{ fontSize: '0.7rem' }}>
              {msg.toolErrorText}
            </Typography>
          </Box>
        )}
      </Box>
    </Stack>
  );
}

function ErrorBubble({ message }: { message: ChatMessage }) {
  return (
    <Stack direction="row" alignItems="flex-start" gap={1} sx={{ mb: 1.5 }} data-testid="chat-error">
      <Box sx={{ width: 28, flexShrink: 0 }} />
      <Box
        sx={{
          width: '100%',
          border: '1px solid',
          borderColor: 'error.main',
          borderRadius: 2,
          px: 1.5,
          py: 1,
          bgcolor: 'rgba(244, 67, 54, 0.08)',
        }}
      >
        <Typography variant="caption" color="error" sx={{ fontSize: '0.75rem' }}>
          {message.content}
        </Typography>
      </Box>
    </Stack>
  );
}

function ThinkingIndicator() {
  const theme = useTheme();
  return (
    <Stack direction="row" alignItems="flex-start" gap={1} sx={{ mb: 1.5 }} data-testid="thinking">
      <Box
        sx={{
          width: 28,
          height: 28,
          borderRadius: '50%',
          bgcolor: theme.vars?.palette.action?.selected ?? 'action.selected',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          flexShrink: 0,
        }}
      >
        <Sparkles size={14} />
      </Box>
      <Box sx={{ px: 2, py: 1.25, borderRadius: '16px 16px 16px 4px', bgcolor: theme.vars?.palette.action?.hover ?? 'action.hover' }}>
        <Stack direction="row" gap={0.5} alignItems="center">
          <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: 'text.disabled', animation: 'pulse 1.4s ease-in-out infinite' }} />
          <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: 'text.disabled', animation: 'pulse 1.4s ease-in-out 0.2s infinite' }} />
          <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: 'text.disabled', animation: 'pulse 1.4s ease-in-out 0.4s infinite' }} />
        </Stack>
      </Box>
    </Stack>
  );
}

// ---------------------------------------------------------------------------
// Greeting / suggestions
// ---------------------------------------------------------------------------

function getGreeting(tab: string): string {
  switch (tab) {
    case 'requirements':
      return 'I can refine the requirements bundle — ask me to add features, tighten FRs, or extend the wireframes.';
    default:
      return 'Chat is currently available on the requirements tab.';
  }
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------

const PANEL_WIDTH = 380;

export default function ChatPanel({ onClose }: { onClose: () => void }) {
  const theme = useTheme();
  const location = useLocation();
  const { orgId, projectId } = useParams();

  const [input, setInput] = useState('');
  const [isSending, setIsSending] = useState(false);
  const [mode, setMode] = useState<'edit' | 'ask'>('edit');
  const [undoingTurn, setUndoingTurn] = useState<string | null>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const tabLabel = resolveTabLabel(location.pathname);
  const tabKey = resolveTabKey(location.pathname);

  // Only the requirements tab gets a real chat session in v1.
  const isChatEnabledTab = tabKey === 'requirements';

  const effectiveOrgId = orgId ?? '';
  const effectiveProjectId = projectId ?? '';
  const projectKey = useMemo(
    () => ({ orgId: effectiveOrgId, projectId: effectiveProjectId }),
    [effectiveOrgId, effectiveProjectId],
  );

  const projectDisplayName = effectiveProjectId
    ? effectiveProjectId.replace(/-/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
    : 'Project';

  const [messages, setMessages] = useState<ChatMessage[]>(() =>
    getChatMessages(effectiveOrgId, effectiveProjectId),
  );
  useEffect(() => {
    setMessages(getChatMessages(effectiveOrgId, effectiveProjectId));
    return subscribeChatStore(() =>
      setMessages(getChatMessages(effectiveOrgId, effectiveProjectId)),
    );
  }, [effectiveOrgId, effectiveProjectId]);

  useEffect(() => {
    setChatPanelOpen(true);
    return () => setChatPanelOpen(false);
  }, []);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, isSending]);

  useEffect(() => {
    setTimeout(() => inputRef.current?.focus(), 100);
  }, []);

  const latestTurnId = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (m.role === 'user' && m.turnStatus === 'completed') return m.turnId;
    }
    return undefined;
  }, [messages]);

  const handleSend = useCallback(async () => {
    const trimmed = input.trim();
    if (!trimmed || isSending || !isChatEnabledTab) return;
    if (!effectiveOrgId || !effectiveProjectId) return;

    setInput('');
    setIsSending(true);
    const userMsg = appendUserMessage(projectKey, trimmed);

    const historyMessages = asChatHistory(getChatMessages(effectiveOrgId, effectiveProjectId)).slice(0, -1);

    let currentTurnId: string | undefined;
    let touchedThisTurn = new Set<string>();

    const finishTurn = (status: UserMessage['turnStatus']) => {
      if (currentTurnId) {
        setTurnStatus(projectKey, currentTurnId, status);
        publishRequirementsPageEvent({
          kind: 'turnEnded',
          turnId: currentTurnId,
          orgId: effectiveOrgId,
          projectId: effectiveProjectId,
        });
      }
      setIsSending(false);
    };

    // Session baseline policy: the BFF holds one snapshot per chat
    // session. The console asks for a fresh capture only when it has none
    // yet (first turn of a session, or after Accept-all dropped it).
    const baselineNow = getSessionBaseline(effectiveOrgId, effectiveProjectId);
    const requestSessionBaseline = !baselineNow;

    const ok = await api.streamRequirementsChat(
      effectiveOrgId,
      effectiveProjectId,
      {
        message: trimmed,
        history: historyMessages,
        mode,
        requestSessionBaseline,
      },
      {
        onTurnStarted: (turnId) => {
          currentTurnId = turnId;
          setUserTurnId(projectKey, userMsg.id, turnId);
          publishRequirementsPageEvent({
            kind: 'turnStarted',
            turnId,
            orgId: effectiveOrgId,
            projectId: effectiveProjectId,
          });
        },
        onSessionBaseline: (snapshotId) => {
          // Persist the baseline id so subsequent turns leave the flag
          // off, and so Accept / Revert (per-file) can reach the snapshot.
          setSessionBaseline(projectKey, snapshotId);
        },
        onText: (delta) => {
          appendAssistantText(projectKey, currentTurnId, delta);
        },
        onToolStarted: (e) => {
          touchedThisTurn.add(e.filename);
          publishRequirementsPageEvent({
            kind: 'busyPathsChanged',
            paths: new Set(touchedThisTurn),
            orgId: effectiveOrgId,
            projectId: effectiveProjectId,
          });
          upsertToolMessage(projectKey, {
            id: e.id,
            role: 'tool',
            content: '',
            timestamp: Date.now(),
            turnId: currentTurnId,
            toolName: e.name as ToolMessage['toolName'],
            toolStatus: 'running',
            toolFilename: e.filename,
            toolSummary: e.summary,
          });
        },
        onToolResult: (e) => {
          // Track primary + sibling writes in the session's modified-set
          // so the banner appears on each affected file (e.g. canvas DSL
          // writes touch both `*.dsl` and the rendered `*.excalidraw`).
          const touched = [e.filename, ...Object.keys(e.siblings ?? {})];
          markFileModified(projectKey, touched, currentTurnId);
          publishRequirementsPageEvent({
            kind: 'fileWritten',
            filename: e.filename,
            content: e.content,
            siblings: e.siblings,
          });
          upsertToolMessage(projectKey, {
            id: e.id,
            role: 'tool',
            content: '',
            timestamp: Date.now(),
            turnId: currentTurnId,
            // toolName is not on the result frame; keep whatever was set by tool-started
            // (upsert overwrites — we need to preserve the name).
            toolName: (
              getChatMessages(effectiveOrgId, effectiveProjectId).find(
                (m) => m.id === e.id && m.role === 'tool',
              ) as ToolMessage | undefined
            )?.toolName ?? 'str_replace',
            toolStatus: 'done',
            toolFilename: e.filename,
            toolSummary: (
              getChatMessages(effectiveOrgId, effectiveProjectId).find(
                (m) => m.id === e.id && m.role === 'tool',
              ) as ToolMessage | undefined
            )?.toolSummary,
            toolDiffStats: e.diff
              ? { added: e.diff.added, removed: e.diff.removed }
              : undefined,
            toolDiffPreview: e.diff?.preview,
          });
        },
        onToolError: (e) => {
          if (!e.id) {
            appendErrorMessage(projectKey, currentTurnId, e.message ?? 'Tool failed', e.errorCode);
            return;
          }
          const prior = getChatMessages(effectiveOrgId, effectiveProjectId).find(
            (m) => m.id === e.id,
          ) as ToolMessage | undefined;
          upsertToolMessage(projectKey, {
            id: e.id,
            role: 'tool',
            content: '',
            timestamp: Date.now(),
            turnId: currentTurnId,
            toolName: prior?.toolName ?? (e.name as ToolMessage['toolName']) ?? 'str_replace',
            toolStatus: 'error',
            toolFilename: e.filename ?? prior?.toolFilename ?? '',
            toolSummary: prior?.toolSummary,
            toolErrorText: e.message,
            toolErrorCode: e.errorCode,
          });
        },
        onValidationFailed: (issues) => {
          appendErrorMessage(
            projectKey,
            currentTurnId,
            `Validation: ${issues.map((i) => `${i.filename ? `${i.filename}: ` : ''}${i.message}`).join('; ')}`,
            'validation',
          );
        },
        onFinish: () => {
          finishTurn('completed');
        },
        onError: (e) => {
          appendErrorMessage(
            projectKey,
            currentTurnId,
            e.errorText ?? 'Chat error',
            e.errorCode,
          );
          finishTurn('failed');
        },
      },
    );
    if (!ok && isSending) {
      finishTurn('failed');
    }
  }, [
    input,
    isSending,
    isChatEnabledTab,
    effectiveOrgId,
    effectiveProjectId,
    projectKey,
    mode,
  ]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleUndo = useCallback(
    async (turnId: string) => {
      if (!effectiveOrgId || !effectiveProjectId) return;
      setUndoingTurn(turnId);
      try {
        const result = await api.undoChatTurn(effectiveOrgId, effectiveProjectId, turnId);
        if (result?.files) {
          markTurnUndone(projectKey, turnId);
          // Tell the page to refresh from the restored snapshot.
          for (const [filename, content] of Object.entries(result.files)) {
            publishRequirementsPageEvent({
              kind: 'fileWritten',
              filename,
              content,
            });
          }
        } else {
          appendErrorMessage(projectKey, turnId, 'Undo failed', 'undo_failed');
        }
      } finally {
        setUndoingTurn(null);
      }
    },
    [effectiveOrgId, effectiveProjectId, projectKey],
  );

  const showGreeting = messages.length === 0;

  return (
    <>
      <style>{`
        @keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
        @keyframes pulse { 0%, 100% { opacity: 0.3; } 50% { opacity: 1; } }
        @keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }
      `}</style>

      <Box
        sx={{
          width: PANEL_WIDTH,
          flexShrink: 0,
          height: '100%',
          display: 'flex',
          flexDirection: 'column',
          bgcolor: theme.vars?.palette.background?.paper ?? 'background.paper',
          borderLeft: '1px solid',
          borderColor: 'divider',
          animation: 'fadeIn 0.18s ease-out',
        }}
        data-testid="chat-panel"
      >
        <Box
          sx={{
            px: 2,
            py: 1.25,
            bgcolor: 'background.paper',
            color: 'text.primary',
            borderBottom: '1px solid',
            borderColor: 'divider',
            flexShrink: 0,
          }}
        >
          <Stack direction="row" alignItems="center" justifyContent="space-between">
            <Stack direction="row" alignItems="center" gap={1}>
              <Sparkles size={18} color={theme.vars?.palette.primary.main as string} />
              <Typography variant="body2" fontWeight={700}>
                Agent Chat
              </Typography>
            </Stack>
            <IconButton size="small" sx={{ color: 'text.secondary' }} onClick={onClose}>
              <X size={16} />
            </IconButton>
          </Stack>
        </Box>

        <Box sx={{ flex: 1, overflowY: 'auto', px: 2, py: 2 }}>
          {showGreeting && (
            <Stack alignItems="center" sx={{ mt: 4, mb: 3, px: 1 }}>
              <Box
                sx={{
                  width: 48,
                  height: 48,
                  borderRadius: '50%',
                  bgcolor: theme.vars?.palette.action?.selected ?? 'action.selected',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  mb: 2,
                }}
              >
                <Sparkles size={24} />
              </Box>
              <Typography variant="body2" fontWeight={600} sx={{ mb: 1, textAlign: 'center' }}>
                Hi! I'm your Agent.
              </Typography>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ textAlign: 'center', lineHeight: 1.6, fontSize: '0.75rem' }}
              >
                {getGreeting(tabKey)}
              </Typography>
            </Stack>
          )}

          {messages.map((msg) => {
            if (msg.role === 'user') {
              const canUndo =
                msg.turnId !== undefined &&
                msg.turnStatus === 'completed' &&
                msg.turnId === latestTurnId;
              return (
                <UserBubble
                  key={msg.id}
                  message={msg}
                  canUndo={canUndo}
                  isUndoing={undoingTurn === msg.turnId}
                  onUndo={() => msg.turnId && handleUndo(msg.turnId)}
                />
              );
            }
            if (msg.role === 'assistant') return <AssistantBubble key={msg.id} content={msg.content} />;
            if (msg.role === 'tool') return <ToolCard key={msg.id} msg={msg} />;
            if (msg.role === 'error') return <ErrorBubble key={msg.id} message={msg} />;
            return null;
          })}

          {isSending && <ThinkingIndicator />}

          <div ref={messagesEndRef} />
        </Box>

        <Divider />

        <Box sx={{ px: 2, pt: 1, pb: 1.5, flexShrink: 0 }}>
          <Stack direction="row" alignItems="center" gap={0.75} sx={{ mb: 1 }}>
            <Typography variant="caption" color="text.secondary" sx={{ fontSize: '0.7rem' }}>
              project:
            </Typography>
            <Chip
              label={projectDisplayName}
              size="small"
              variant="outlined"
              sx={{ height: 20, fontSize: '0.65rem', fontWeight: 600, '& .MuiChip-label': { px: 0.75 } }}
            />
            <Typography variant="caption" color="text.secondary" sx={{ fontSize: '0.7rem' }}>
              context:
            </Typography>
            <Chip
              label={tabLabel}
              size="small"
              color="primary"
              variant="outlined"
              sx={{ height: 20, fontSize: '0.65rem', fontWeight: 600, '& .MuiChip-label': { px: 0.75 } }}
            />
            {isChatEnabledTab && (
              <Chip
                label={mode === 'edit' ? 'mode: edit' : 'mode: ask'}
                size="small"
                variant="outlined"
                onClick={() => setMode((m) => (m === 'edit' ? 'ask' : 'edit'))}
                sx={{ height: 20, fontSize: '0.65rem', fontWeight: 600, cursor: 'pointer', '& .MuiChip-label': { px: 0.75 } }}
                data-testid="mode-toggle"
              />
            )}
          </Stack>
          <Stack direction="row" alignItems="flex-end" gap={1}>
            <TextField
              inputRef={inputRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder={
                isChatEnabledTab
                  ? mode === 'edit'
                    ? 'Describe a change to the requirements...'
                    : 'Ask a question about the requirements...'
                  : 'Chat is currently available on the requirements tab.'
              }
              disabled={!isChatEnabledTab || isSending}
              multiline
              minRows={3}
              maxRows={6}
              fullWidth
              size="small"
              data-testid="chat-input"
              sx={{
                '& .MuiOutlinedInput-root': {
                  borderRadius: 1,
                  fontSize: '0.8125rem',
                  alignItems: 'flex-start',
                },
              }}
            />
            <IconButton
              color="primary"
              onClick={handleSend}
              disabled={!input.trim() || isSending || !isChatEnabledTab}
              data-testid="chat-send"
              sx={{
                bgcolor: input.trim() && !isSending ? (theme.vars?.palette.primary.main ?? 'primary.main') : undefined,
                color: input.trim() && !isSending ? (theme.vars?.palette.primary.contrastText ?? '#fff') : undefined,
                width: 36,
                height: 36,
                '&:hover': input.trim() && !isSending
                  ? { bgcolor: theme.vars?.palette.primary.dark ?? 'primary.dark' }
                  : {},
              }}
            >
              {isSending ? <CircularProgress size={16} /> : <ArrowUp size={18} />}
            </IconButton>
          </Stack>
        </Box>
      </Box>
    </>
  );
}
