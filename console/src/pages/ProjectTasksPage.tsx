import { useEffect, useRef, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Accordion, AccordionDetails, AccordionSummary, alpha, Box, Button, CircularProgress, IconButton, PageContent, Tooltip, Typography, useTheme } from '@wso2/oxygen-ui';
import { AlertTriangle, CheckCircle, ChevronDown, ChevronRight, Github, OctagonAlert } from '@wso2/oxygen-ui-icons-react';
import { useProjectBoard } from '../hooks/useProjectBoard';
import { TasksPageHeader } from '../components/tasks/TasksPageHeader';
import { TaskDetailPopup } from '../components/tasks/TaskDetailPopup';
import { LabelList } from '../components/tasks/LabelList';
import type { Task } from '../services/api';

// ── Section config ──────────────────────────────────────────────────────────

interface SectionConfig {
  key: 'inProgress' | 'todo' | 'done' | 'onHold' | 'failed';
  label: string;
  isPrimary: boolean;
  dotColor: string | null;
  borderColor: string | null;
}

const SECTIONS: SectionConfig[] = [
  { key: 'inProgress', label: 'In Progress', isPrimary: true,  dotColor: 'primary',    borderColor: null         },
  { key: 'todo',       label: 'To Do',        isPrimary: false, dotColor: null,         borderColor: null         },
  { key: 'done',       label: 'Done',         isPrimary: false, dotColor: null,         borderColor: null         },
  { key: 'onHold',     label: 'On Hold',      isPrimary: false, dotColor: null,         borderColor: null         },
  { key: 'failed',     label: 'Failed',       isPrimary: false, dotColor: 'error.main', borderColor: 'error.main' },
];

// ── TaskRow ─────────────────────────────────────────────────────────────────

interface TaskRowProps {
  task: Task;
  section: SectionConfig;
  orgId: string;
  projectId: string;
}

function TaskRow({ task, section, orgId, projectId }: TaskRowProps) {
  const theme = useTheme();
  const isDark = theme.palette.mode === 'dark';
  const rowRef = useRef<HTMLDivElement>(null);
  const [popupOpen, setPopupOpen] = useState(false);

  const lifecycle = task.lifecycleStatus ?? 'gh_issue_created';
  const isWaiting  = lifecycle === 'gh_issue_waiting';
  const isSyncing  = lifecycle === 'gh_issue_syncing';
  const isFailed   = lifecycle === 'gh_issue_failed';

  // Syncing card while issue creation is in flight or board hasn't synced yet
  if (isWaiting || isSyncing) {
    return (
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 1.5,
          px: 2,
          py: 1.5,
          borderRadius: 1.25,
          border: '1px solid',
          borderColor: isDark ? 'rgba(255,255,255,0.07)' : 'rgba(0,0,0,0.07)',
          bgcolor: 'background.paper',
          opacity: 0.55,
        }}
      >
        {/* Gray pulsing dot */}
        <Box sx={{ flexShrink: 0, width: 8, height: 8, position: 'relative' }}>
          <Box
            sx={{
              width: 8,
              height: 8,
              borderRadius: '50%',
              bgcolor: 'text.disabled',
              position: 'relative',
              zIndex: 1,
            }}
          />
          <Box
            aria-hidden
            sx={{
              position: 'absolute',
              inset: 0,
              borderRadius: '50%',
              bgcolor: 'text.disabled',
              opacity: 0.5,
              animation: 'ping 1.4s ease-out infinite',
              '@keyframes ping': {
                '0%':   { transform: 'scale(1)',   opacity: 0.5 },
                '100%': { transform: 'scale(2.8)', opacity: 0   },
              },
            }}
          />
        </Box>

        {/* Syncing message */}
        <Typography
          sx={{
            fontSize: '0.875rem',
            fontWeight: 450,
            flex: 1,
            color: 'text.disabled',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            fontStyle: 'italic',
          }}
        >
          Task created — syncing with project board…
        </Typography>
      </Box>
    );
  }

  return (
    <>
      <Box
        ref={rowRef}
        onClick={() => setPopupOpen(p => !p)}
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 1.5,
          px: 2,
          py: 1.5,
          cursor: 'pointer',
          borderRadius: 1.25,
          border: '1px solid',
          borderColor: isFailed
            ? 'error.main'
            : popupOpen
              ? 'primary.main'
              : isDark ? 'rgba(255,255,255,0.07)' : 'rgba(0,0,0,0.07)',
          ...(isFailed && {
            borderLeft: '3px solid',
            borderLeftColor: 'error.main',
          }),
          ...(!isFailed && section.borderColor && {
            borderLeft: '3px solid',
            borderLeftColor: section.borderColor,
          }),
          ...(!isFailed && section.isPrimary && {
            borderLeft: '3px solid',
            borderLeftColor: 'primary.main',
          }),
          bgcolor: isFailed ? (t) => alpha(t.palette.error.main, 0.04) : 'background.paper',
          transition: 'border-color 0.15s, background-color 0.15s',
          '&:hover': {
            borderColor: isFailed
              ? 'error.dark'
              : popupOpen
                ? 'primary.main'
                : isDark ? 'rgba(255,255,255,0.13)' : 'rgba(0,0,0,0.13)',
            bgcolor: isFailed
              ? (t) => alpha(t.palette.error.main, 0.07)
              : isDark ? 'rgba(255,255,255,0.015)' : 'rgba(0,0,0,0.01)',
          },
        }}
      >
        {/* Status dot */}
        <Box sx={{ flexShrink: 0, width: 8, height: 8, position: 'relative' }}>
          {section.dotColor && !isFailed && (
            <>
              <Box
                sx={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  bgcolor: section.isPrimary ? 'primary.main' : section.dotColor,
                  position: 'relative',
                  zIndex: 1,
                }}
              />
              {section.isPrimary && (
                <Box
                  aria-hidden
                  sx={{
                    position: 'absolute',
                    inset: 0,
                    borderRadius: '50%',
                    bgcolor: 'primary.main',
                    opacity: 0.6,
                    animation: 'ping 1.4s ease-out infinite',
                    '@keyframes ping': {
                      '0%':    { transform: 'scale(1)',   opacity: 0.6 },
                      '100%':  { transform: 'scale(2.8)', opacity: 0   },
                    },
                  }}
                />
              )}
            </>
          )}
        </Box>

        {/* Title */}
        <Typography
          sx={{
            fontSize: '0.875rem',
            fontWeight: 450,
            flex: 1,
            color: isFailed ? 'error.main' : 'text.primary',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {task.title}
        </Typography>

        {/* Labels */}
        {task.labels && task.labels.length > 0 && (
          <Box sx={{ flexShrink: 0 }}>
            <LabelList labels={task.labels} />
          </Box>
        )}

        {/* GitHub link or issue-failed indicator */}
        {isFailed ? (
          <Tooltip title="GitHub issue creation failed">
            <Box sx={{ display: 'flex', color: 'error.main', p: 0.5 }}>
              <OctagonAlert size={14} />
            </Box>
          </Tooltip>
        ) : task.url ? (
          <Tooltip title="Open in GitHub">
            <IconButton
              component="a"
              href={task.url}
              target="_blank"
              rel="noopener noreferrer"
              size="small"
              onClick={(e: React.MouseEvent) => e.stopPropagation()}
              sx={{ p: 0.5, color: 'text.disabled', '&:hover': { color: 'text.secondary' } }}
            >
              <Github size={14} />
            </IconButton>
          </Tooltip>
        ) : null}

        {/* Chevron indicator */}
        <Box sx={{ flexShrink: 0 }}>
          {popupOpen ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </Box>
      </Box>

      <TaskDetailPopup
        open={popupOpen}
        anchorEl={rowRef.current}
        task={task}
        isInTodo={section.key === 'todo'}
        orgId={orgId}
        projectId={projectId}
        onClose={() => setPopupOpen(false)}
      />
    </>
  );
}

// ── Section ─────────────────────────────────────────────────────────────────

interface TaskSectionProps {
  section: SectionConfig;
  tasks: Task[];
  orgId: string;
  projectId: string;
  initiallyExpanded: boolean;
}

function TaskSection({ section, tasks, orgId, projectId, initiallyExpanded }: TaskSectionProps) {
  const theme = useTheme();
  const [expanded, setExpanded] = useState(initiallyExpanded);

  const filteredTasks = tasks;

  const labelColor = section.isPrimary ? theme.palette.primary.main : theme.palette.text.secondary;

  return (
    <Accordion
      expanded={expanded}
      onChange={(_, val) => setExpanded(val)}
      disableGutters
      elevation={0}
      sx={{
        mb: 0.75,
        border: '1px solid',
        borderColor: 'divider',
        borderRadius: 1.25,
        '&:before': { display: 'none' },
        overflow: 'hidden',
      }}
    >
      <AccordionSummary
        expandIcon={<ChevronDown size={14} style={{ color: labelColor }} />}
        sx={{
          minHeight: 40,
          px: 1.75,
          py: 0,
          '& .MuiAccordionSummary-content': { my: 1, alignItems: 'center', gap: 0.75 },
        }}
      >
        <Typography
          sx={{ fontSize: '0.72rem', fontWeight: 700, color: labelColor, textTransform: 'uppercase', letterSpacing: '0.06em' }}
        >
          {section.label}
        </Typography>
        <Typography
          sx={{ fontSize: '0.72rem', fontWeight: 500, color: labelColor, opacity: 0.7 }}
        >
          {tasks.length}
        </Typography>
      </AccordionSummary>

      <AccordionDetails sx={{ p: 1.25, display: 'flex', flexDirection: 'column', gap: 0.75 }}>
        {/* Task rows */}
        {filteredTasks.length > 0 ? (
          filteredTasks.map(task => (
            <TaskRow
              key={task.id}
              task={task}
              section={section}
              orgId={orgId}
              projectId={projectId}
            />
          ))
        ) : (
          <Box sx={{ py: 2, display: 'flex', justifyContent: 'center' }}>
            <Typography sx={{ fontSize: '0.75rem', color: 'text.disabled' }}>
              {tasks.length > 0 ? 'No matching tasks' : 'No tasks'}
            </Typography>
          </Box>
        )}
      </AccordionDetails>
    </Accordion>
  );
}

// ── Page ─────────────────────────────────────────────────────────────────────

export default function ProjectTasksPage() {
  const { orgId, projectId } = useParams<{ orgId: string; projectId: string }>();

  const failedRef = useRef<HTMLDivElement>(null);

  const {
    board,
    isLoading,
    error,
    isGenerating,
    isDispatching,
    actionError,
    totalTasks,
    handleGenerate,
    handleStartImplementation,
    clearActionError,
    generateBanner,
    hideGenerateButton,
    clearGenerateBanner,
  } = useProjectBoard(orgId, projectId);

  const navigate = useNavigate();

  useEffect(() => {
    if (generateBanner?.autoDismiss) {
      const t = setTimeout(() => clearGenerateBanner(), 5000);
      return () => clearTimeout(t);
    }
    return undefined;
  }, [generateBanner, clearGenerateBanner]);


  if (isLoading) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', pt: 16, gap: 1.5 }}>
          <CircularProgress size={28} thickness={3} />
          <Typography variant="body2" color="text.disabled">Loading tasks…</Typography>
        </Box>
      </PageContent>
    );
  }

  if (error) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', justifyContent: 'center', pt: 16 }}>
          <Typography variant="body2" color="error.main">{error}</Typography>
        </Box>
      </PageContent>
    );
  }

  const failedCount    = board.failed.length;
  const inProgressCount = board.inProgress.length;
  const todoCount      = board.todo.length;

  // Determine which banner to show (mutually exclusive, priority order).
  type BannerVariant = 'failed' | 'in_progress' | 'all_done' | null;
  let bannerVariant: BannerVariant = null;
  if (totalTasks > 0) {
    if (failedCount > 0)               bannerVariant = 'failed';
    else if (inProgressCount > 0)      bannerVariant = 'in_progress';
    else if (todoCount === 0)          bannerVariant = 'all_done';
  }

  return (
    <PageContent sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Box sx={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0, overflow: 'hidden' }}>
      <TasksPageHeader
        projectId={projectId ?? ''}
        totalTasks={totalTasks}
        isGenerating={isGenerating}
        isDispatching={isDispatching}
        githubProjectUrl={board.url}
        hideGenerateButton={hideGenerateButton}
        onGenerate={handleGenerate}
        onStartImplementation={handleStartImplementation}
      />

      {generateBanner && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2,
            py: 1.5,
            mb: 2,
            borderRadius: 1.25,
            bgcolor: (t) =>
              generateBanner.variant === 'info'    ? alpha(t.palette.info.main,    0.08) :
              generateBanner.variant === 'warning' ? alpha(t.palette.warning.main, 0.08) :
              generateBanner.variant === 'success' ? alpha(t.palette.success.main, 0.08) :
                                                     alpha(t.palette.error.main,   0.08),
            border: '1px solid',
            borderColor: (t) =>
              generateBanner.variant === 'info'    ? alpha(t.palette.info.main,    0.2) :
              generateBanner.variant === 'warning' ? alpha(t.palette.warning.main, 0.2) :
              generateBanner.variant === 'success' ? alpha(t.palette.success.main, 0.2) :
                                                     alpha(t.palette.error.main,   0.2),
          }}
        >
          <Box
            sx={{
              flexShrink: 0,
              display: 'flex',
              color:
                generateBanner.variant === 'info'    ? 'info.main' :
                generateBanner.variant === 'warning' ? 'warning.main' :
                generateBanner.variant === 'success' ? 'success.main' : 'error.main',
            }}
          >
            {generateBanner.variant === 'info'    ? <CircularProgress size={16} sx={{ color: 'info.main' }} /> :
             generateBanner.variant === 'success' ? <CheckCircle size={16} /> :
                                                    <AlertTriangle size={16} />}
          </Box>

          <Box sx={{ flex: 1 }}>
            <Typography
              variant="body2"
              sx={{
                color:
                  generateBanner.variant === 'info'    ? 'info.main' :
                  generateBanner.variant === 'warning' ? 'warning.main' :
                  generateBanner.variant === 'success' ? 'success.main' : 'error.main',
                lineHeight: 1.3,
              }}
            >
              {generateBanner.message}
            </Typography>
          </Box>

          {generateBanner.action && (
            <Button
              size="small"
              variant="outlined"
              color={generateBanner.variant === 'warning' ? 'warning' : 'error'}
              onClick={() => navigate(generateBanner.action!.path)}
              sx={{ flexShrink: 0, whiteSpace: 'nowrap' }}
            >
              {generateBanner.action.label}
            </Button>
          )}

          <IconButton
            size="small"
            onClick={clearGenerateBanner}
            sx={{
              p: 0.5,
              color:
                generateBanner.variant === 'info'    ? 'info.main' :
                generateBanner.variant === 'warning' ? 'warning.main' :
                generateBanner.variant === 'success' ? 'success.main' : 'error.main',
            }}
          >
            ×
          </IconButton>
        </Box>
      )}

      {actionError && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2,
            py: 1.5,
            mb: 2,
            borderRadius: 1.25,
            bgcolor: (t) => alpha(t.palette.error.main, 0.08),
            border: '1px solid',
            borderColor: (t) => alpha(t.palette.error.main, 0.2),
          }}
        >
          <Box sx={{ flex: 1 }}>
            <Typography variant="body2" sx={{ color: 'error.main', lineHeight: 1.3 }}>
              {actionError}
            </Typography>
          </Box>
          <IconButton
            size="small"
            onClick={clearActionError}
            sx={{ color: 'error.main', p: 0.5 }}
          >
            ×
          </IconButton>
        </Box>
      )}

      {/* Contextual banner */}
      {bannerVariant === 'failed' && (
        <Box
          onClick={() => failedRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2,
            py: 1.5,
            mb: 2,
            borderRadius: 1.25,
            bgcolor: (t) => alpha(t.palette.error.main, 0.08),
            border: '1px solid',
            borderColor: (t) => alpha(t.palette.error.main, 0.2),
            cursor: 'pointer',
            transition: 'all 0.15s',
            '&:hover': {
              bgcolor: (t) => alpha(t.palette.error.main, 0.12),
              borderColor: (t) => alpha(t.palette.error.main, 0.3),
            },
          }}
        >
          <Box sx={{ flexShrink: 0, color: 'error.main', display: 'flex' }}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2L1 21h22L12 2zm0 3.5L20.5 19H3.5L12 5.5zM11 10v4h2v-4h-2zm0 6v2h2v-2h-2z"/></svg>
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 700, color: 'error.main', lineHeight: 1.3 }}>
              {failedCount} task{failedCount !== 1 ? 's' : ''} failed
            </Typography>
            <Typography variant="caption" sx={{ color: 'error.dark', lineHeight: 1.3 }}>
              Click to review failed tasks
            </Typography>
          </Box>
          <Box sx={{ flexShrink: 0, color: 'error.main', display: 'flex' }}>
            <ChevronRight size={16} />
          </Box>
        </Box>
      )}

      {bannerVariant === 'in_progress' && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2,
            py: 1.5,
            mb: 2,
            borderRadius: 1.25,
            bgcolor: (t) => alpha(t.palette.info.main, 0.08),
            border: '1px solid',
            borderColor: (t) => alpha(t.palette.info.main, 0.2),
          }}
        >
          <Box sx={{ flexShrink: 0, display: 'flex' }}>
            <CircularProgress size={16} sx={{ color: 'info.main' }} />
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 700, color: 'info.main', lineHeight: 1.3 }}>
              {inProgressCount} task{inProgressCount !== 1 ? 's' : ''} in progress
            </Typography>
            <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
              All tasks are dispatched to the agents and they are working on it
            </Typography>
          </Box>
        </Box>
      )}

      {bannerVariant === 'all_done' && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2,
            py: 1.5,
            mb: 2,
            borderRadius: 1.25,
            bgcolor: (t) => alpha(t.palette.success.main, 0.08),
            border: '1px solid',
            borderColor: (t) => alpha(t.palette.success.main, 0.2),
          }}
        >
          <Box sx={{ flexShrink: 0, color: 'success.main', display: 'flex' }}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41L9 16.17z"/></svg>
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 700, color: 'success.main', lineHeight: 1.3 }}>
              All pending tasks are complete
            </Typography>
            <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
              All tasks have been completed successfully
            </Typography>
          </Box>
        </Box>
      )}

      {/* Task sections */}
      <Box sx={{ flex: 1, overflowY: 'auto', pr: 0.25 }}>
        {totalTasks === 0 && (
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1.5,
              px: 2,
              py: 1.5,
              mb: 2,
              borderRadius: 1.25,
              bgcolor: (t) => alpha(t.palette.info.main, 0.08),
              border: '1px solid',
              borderColor: (t) => alpha(t.palette.info.main, 0.2),
            }}
          >
            <Box sx={{ flexShrink: 0, display: 'flex', color: 'info.main' }}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
              </svg>
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography variant="body2" sx={{ fontWeight: 700, color: 'info.main', lineHeight: 1.3 }}>
                No tasks generated yet
              </Typography>
              <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
                Tasks haven't been generated for this project. Generate tasks to get started.
              </Typography>
            </Box>
          </Box>
        )}

        {SECTIONS.map(section => (
          <Box key={section.key} ref={section.key === 'failed' ? failedRef : undefined}>
            <TaskSection
              section={section}
              tasks={board[section.key]}
              orgId={orgId ?? ''}
              projectId={projectId ?? ''}
              initiallyExpanded={section.key === 'todo' || section.key === 'inProgress'}
            />
          </Box>
        ))}
      </Box>
      </Box>
    </PageContent>
  );
}
