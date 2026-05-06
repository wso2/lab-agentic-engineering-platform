/**
 * MOCK: Agent chat panel.
 *
 * A right-side panel that lets users iterate on project artifacts with AI.
 * Shows tool call cards, streaming-style mock responses.
 * Project and context info is shown in the input section.
 * Messages persist in memory across page navigations.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { useLocation, useParams } from 'react-router-dom';
import {
  Box,
  Chip,
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
  Sparkles,
  Wrench,
  X,
} from '@wso2/oxygen-ui-icons-react';
import {
  type ChatMessage,
  addChatMessage,
  emitPageUpdate,
  getChatMessages,
  nextMessageId,
  setChatPanelOpen,
  subscribeChatStore,
  updateChatMessage,
} from '../services/chatStore';

// ---------------------------------------------------------------------------
// Tab label resolver
// ---------------------------------------------------------------------------

function resolveTabLabel(pathname: string): string {
  if (pathname.includes('/requirements')) return 'Requirements';
  if (pathname.includes('/architecture')) return 'Architecture';
  if (pathname.includes('/implementation-plan')) return 'Implementation Plan';
  if (pathname.includes('/components/')) return 'Component';
  if (pathname.includes('/components')) return 'Components';
  if (pathname.includes('/prompt')) return 'Prompt';
  if (pathname.includes('/spec')) return 'Specification';
  if (pathname.includes('/build')) return 'Build';
  if (pathname.includes('/deploy')) return 'Deploy';
  return 'Overview';
}

function resolveTabKey(pathname: string): string {
  if (pathname.includes('/requirements')) return 'requirements';
  if (pathname.includes('/architecture')) return 'architecture';
  if (pathname.includes('/implementation-plan')) return 'implementation-plan';
  if (pathname.includes('/components')) return 'components';
  if (pathname.includes('/prompt')) return 'prompt';
  return 'overview';
}

// ---------------------------------------------------------------------------
// MOCK: Canned AI responses per tab
// ---------------------------------------------------------------------------

interface MockResponse {
  toolName: string;
  toolSummary: string;
  reply: string;
  pageUpdateAction?: string;
  pageUpdateData?: string;
}

function getMockResponse(tab: string, userMsg: string): MockResponse {
  const lower = userMsg.toLowerCase();

  if (tab === 'requirements') {
    if (lower.includes('payroll') || lower.includes('salary')) {
      return {
        toolName: 'update_requirements',
        toolSummary: 'Added "Payroll & Compensation" section with 4 capabilities',
        reply: 'Done! I\'ve added a new **Payroll & Compensation** section to the requirements with salary viewing, payslip downloads, tax document access, and compensation history tracking.',
        pageUpdateAction: 'append',
        pageUpdateData: `\n\n## Payroll & Compensation\n- View monthly salary breakdown and deductions\n- Download payslips as PDF for any previous month\n- Access tax documents (Form 16, annual statements)\n- View compensation history and increment timeline`,
      };
    }
    if (lower.includes('meeting') || lower.includes('room') || lower.includes('booking')) {
      return {
        toolName: 'update_requirements',
        toolSummary: 'Added "Meeting Room Booking" section with 4 capabilities',
        reply: 'I\'ve added **Meeting Room Booking** capabilities. Employees can now book rooms, see availability, and get reminders.',
        pageUpdateAction: 'append',
        pageUpdateData: `\n\n## Meeting Room Booking\n- Search and book available meeting rooms by floor, capacity, and amenities\n- View real-time room availability calendar\n- Recurring booking support for standup and team meetings\n- Automated reminders 5 minutes before the meeting`,
      };
    }
    return {
      toolName: 'update_requirements',
      toolSummary: `Updated requirements based on: "${userMsg.slice(0, 50)}..."`,
      reply: `I've updated the requirements to incorporate your feedback. The changes are reflected in the document on the left.`,
      pageUpdateAction: 'append',
      pageUpdateData: `\n\n## Additional Requirements\n- ${userMsg}`,
    };
  }

  if (tab === 'architecture') {
    return {
      toolName: 'update_architecture',
      toolSummary: 'Modified component architecture based on feedback',
      reply: `Good suggestion! I've updated the architecture to account for that. The component diagram and details on the left have been refreshed.`,
    };
  }

  if (tab === 'implementation-plan') {
    return {
      toolName: 'update_implementation_plan',
      toolSummary: 'Adjusted implementation plan phases and ordering',
      reply: `I've restructured the implementation plan based on your input. The phase ordering and component assignments have been updated.`,
    };
  }

  if (tab === 'components') {
    return {
      toolName: 'update_component',
      toolSummary: 'Modified component configuration',
      reply: `I've applied the changes to the component. You can review the updated details on the left.`,
    };
  }

  return {
    toolName: 'analyze_project',
    toolSummary: 'Analyzed project context',
    reply: `I understand. I've analyzed the current state and the context looks good. Let me know if you'd like me to make any specific changes.`,
  };
}

// ---------------------------------------------------------------------------
// Greeting message
// ---------------------------------------------------------------------------

function getGreeting(tab: string): string {
  switch (tab) {
    case 'requirements':
      return 'I can help you refine the requirements. Try asking me to add new capabilities, modify existing ones, or restructure sections.';
    case 'architecture':
      return 'I can help you adjust the architecture — add components, change technology choices, or modify service boundaries.';
    case 'implementation-plan':
      return 'I can help you reorder phases, adjust component responsibilities, or modify the implementation strategy.';
    case 'components':
      return 'I can help you modify component configurations, update responsibilities, or adjust API boundaries.';
    default:
      return 'How can I help you with this project?';
  }
}

// ---------------------------------------------------------------------------
// Message bubble components
// ---------------------------------------------------------------------------

function UserBubble({ message }: { message: ChatMessage }) {
  const theme = useTheme();
  return (
    <Stack alignItems="flex-end" sx={{ mb: 1.5 }}>
      <Box
        sx={{
          maxWidth: '85%',
          px: 2,
          py: 1.25,
          borderRadius: '16px 16px 4px 16px',
          bgcolor: theme.vars?.palette.primary.main ?? 'primary.main',
          color: theme.vars?.palette.primary.contrastText ?? '#fff',
        }}
      >
        <Typography variant="body2" sx={{ lineHeight: 1.6, fontSize: '0.8125rem' }}>
          {message.content}
        </Typography>
      </Box>
    </Stack>
  );
}

function AssistantBubble({ message }: { message: ChatMessage }) {
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
        <Typography variant="body2" sx={{ lineHeight: 1.6, fontSize: '0.8125rem' }}>
          {renderMarkdownLite(message.content)}
        </Typography>
      </Box>
    </Stack>
  );
}

function ToolCallCard({ message }: { message: ChatMessage }) {
  const theme = useTheme();
  const isRunning = message.toolStatus === 'running';

  return (
    <Stack direction="row" alignItems="flex-start" gap={1} sx={{ mb: 1.5 }}>
      <Box sx={{ width: 28, flexShrink: 0 }} />
      <Box
        sx={{
          width: '100%',
          border: '1px solid',
          borderColor: isRunning
            ? (theme.vars?.palette.warning?.main ?? 'warning.main')
            : (theme.vars?.palette.success?.main ?? 'success.main'),
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
            bgcolor: isRunning
              ? 'rgba(255, 152, 0, 0.06)'
              : 'rgba(76, 175, 80, 0.06)',
          }}
        >
          {isRunning ? (
            <Loader2 size={14} style={{ animation: 'spin 1s linear infinite' }} />
          ) : (
            <Check size={14} color={theme.vars?.palette.success?.main as string ?? '#4caf50'} />
          )}
          <Wrench size={12} style={{ opacity: 0.5 }} />
          <Typography variant="caption" fontWeight={600} sx={{ fontSize: '0.75rem' }}>
            {message.toolName}
          </Typography>
        </Stack>
        {message.toolSummary && (
          <Box sx={{ px: 1.5, py: 0.75, borderTop: '1px solid', borderColor: 'divider' }}>
            <Typography variant="caption" color="text.secondary" sx={{ fontSize: '0.7rem' }}>
              {message.toolSummary}
            </Typography>
          </Box>
        )}
      </Box>
    </Stack>
  );
}

function ThinkingIndicator() {
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
        }}
      >
        <Sparkles size={14} />
      </Box>
      <Box
        sx={{
          px: 2,
          py: 1.25,
          borderRadius: '16px 16px 16px 4px',
          bgcolor: theme.vars?.palette.action?.hover ?? 'action.hover',
        }}
      >
        <Stack direction="row" gap={0.5} alignItems="center">
          <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: 'text.disabled', animation: 'pulse 1.4s ease-in-out infinite' }} />
          <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: 'text.disabled', animation: 'pulse 1.4s ease-in-out 0.2s infinite' }} />
          <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: 'text.disabled', animation: 'pulse 1.4s ease-in-out 0.4s infinite' }} />
        </Stack>
      </Box>
    </Stack>
  );
}

// Simple markdown bold renderer
function renderMarkdownLite(text: string): React.ReactNode {
  const parts = text.split(/(\*\*[^*]+\*\*)/g);
  return parts.map((part, i) => {
    if (part.startsWith('**') && part.endsWith('**')) {
      return <strong key={i}>{part.slice(2, -2)}</strong>;
    }
    return part;
  });
}

// ---------------------------------------------------------------------------
// Main ChatPanel component
// ---------------------------------------------------------------------------

const PANEL_WIDTH = 380;

export default function ChatPanel({ onClose }: { onClose: () => void }) {
  const theme = useTheme();
  const location = useLocation();
  const { projectId } = useParams();

  const [input, setInput] = useState('');
  const [isThinking, setIsThinking] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const tabLabel = resolveTabLabel(location.pathname);
  const tabKey = resolveTabKey(location.pathname);
  const chatProjectId = projectId ?? '';

  // MOCK: Derive project display name from projectId
  const projectDisplayName = projectId
    ? projectId.replace(/-/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
    : 'Project';

  // Subscribe to chat store
  const [messages, setMessages] = useState<ChatMessage[]>(() => getChatMessages(chatProjectId));
  useEffect(() => {
    setMessages(getChatMessages(chatProjectId));
    return subscribeChatStore(() => setMessages(getChatMessages(chatProjectId)));
  }, [chatProjectId]);

  // MOCK: Sync panel state
  useEffect(() => {
    setChatPanelOpen(true);
    return () => setChatPanelOpen(false);
  }, []);

  // Auto-scroll to bottom on new messages
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, isThinking]);

  // Focus input when panel opens
  useEffect(() => {
    setTimeout(() => inputRef.current?.focus(), 100);
  }, []);

  const handleSend = useCallback(() => {
    const trimmed = input.trim();
    if (!trimmed || isThinking) return;

    // Add user message
    addChatMessage(chatProjectId, {
      id: nextMessageId(),
      role: 'user',
      content: trimmed,
      timestamp: Date.now(),
    });
    setInput('');
    setIsThinking(true);

    const mock = getMockResponse(tabKey, trimmed);

    // MOCK: Simulate tool call after a short delay
    const toolMsgId = nextMessageId();
    setTimeout(() => {
      addChatMessage(chatProjectId, {
        id: toolMsgId,
        role: 'tool',
        content: '',
        toolName: mock.toolName,
        toolSummary: mock.toolSummary,
        toolStatus: 'running',
        timestamp: Date.now(),
      });
    }, 800);

    // MOCK: Tool call completes, emit page update
    setTimeout(() => {
      updateChatMessage(chatProjectId, toolMsgId, { toolStatus: 'done' });

      if (mock.pageUpdateAction && mock.pageUpdateData) {
        emitPageUpdate({
          tab: tabKey,
          action: mock.pageUpdateAction,
          data: mock.pageUpdateData,
        });
      }
    }, 2000);

    // MOCK: AI response
    setTimeout(() => {
      setIsThinking(false);
      addChatMessage(chatProjectId, {
        id: nextMessageId(),
        role: 'assistant',
        content: mock.reply,
        timestamp: Date.now(),
      });
    }, 2800);
  }, [input, isThinking, chatProjectId, tabKey]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  // Determine if we should show the greeting (no messages yet for this project)
  const showGreeting = messages.length === 0;

  return (
    <>
      {/* CSS for animations */}
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
      >
        {/* Header */}
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

        {/* Messages area */}
        <Box
          sx={{
            flex: 1,
            overflowY: 'auto',
            px: 2,
            py: 2,
          }}
        >
          {/* Greeting */}
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

              {/* Quick suggestion chips */}
              <Stack direction="row" gap={0.75} flexWrap="wrap" justifyContent="center" sx={{ mt: 2.5 }}>
                {tabKey === 'requirements' && (
                  <>
                    <SuggestionChip label="Add payroll module" onSelect={(t) => { setInput(t); }} />
                    <SuggestionChip label="Add meeting room booking" onSelect={(t) => { setInput(t); }} />
                  </>
                )}
                {tabKey === 'architecture' && (
                  <>
                    <SuggestionChip label="Add a caching layer" onSelect={(t) => { setInput(t); }} />
                    <SuggestionChip label="Use event-driven pattern" onSelect={(t) => { setInput(t); }} />
                  </>
                )}
                {tabKey === 'implementation-plan' && (
                  <>
                    <SuggestionChip label="Parallelize phase 1" onSelect={(t) => { setInput(t); }} />
                    <SuggestionChip label="Add integration tests phase" onSelect={(t) => { setInput(t); }} />
                  </>
                )}
              </Stack>
            </Stack>
          )}

          {/* Message list */}
          {messages.map((msg) => {
            switch (msg.role) {
              case 'user':
                return <UserBubble key={msg.id} message={msg} />;
              case 'assistant':
                return <AssistantBubble key={msg.id} message={msg} />;
              case 'tool':
                return <ToolCallCard key={msg.id} message={msg} />;
              default:
                return null;
            }
          })}

          {/* Thinking indicator */}
          {isThinking && <ThinkingIndicator />}

          <div ref={messagesEndRef} />
        </Box>

        <Divider />

        {/* Input area with project/context info */}
        <Box sx={{ px: 2, pt: 1, pb: 1.5, flexShrink: 0 }}>
          {/* Context tags */}
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
          </Stack>
          <Stack direction="row" alignItems="flex-end" gap={1}>
            <TextField
              inputRef={inputRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Ask about this step..."
              multiline
              minRows={3}
              maxRows={6}
              fullWidth
              size="small"
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
              disabled={!input.trim() || isThinking}
              sx={{
                bgcolor: input.trim() ? (theme.vars?.palette.primary.main ?? 'primary.main') : undefined,
                color: input.trim() ? (theme.vars?.palette.primary.contrastText ?? '#fff') : undefined,
                width: 36,
                height: 36,
                '&:hover': input.trim()
                  ? { bgcolor: theme.vars?.palette.primary.dark ?? 'primary.dark' }
                  : {},
              }}
            >
              <ArrowUp size={18} />
            </IconButton>
          </Stack>
        </Box>
      </Box>
    </>
  );
}

// ---------------------------------------------------------------------------
// Suggestion chip
// ---------------------------------------------------------------------------

function SuggestionChip({ label, onSelect }: { label: string; onSelect: (text: string) => void }) {
  return (
    <Chip
      label={label}
      size="small"
      variant="outlined"
      onClick={() => onSelect(label)}
      sx={{
        cursor: 'pointer',
        fontSize: '0.7rem',
        height: 26,
        '&:hover': { bgcolor: 'action.hover' },
      }}
    />
  );
}
