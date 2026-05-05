import { useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Accordion, AccordionDetails, AccordionSummary, Box, CircularProgress, IconButton, Tooltip, Typography, useTheme } from '@wso2/oxygen-ui';
import { ChevronDown, ChevronRight, Github } from 'lucide-react';
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
  { key: 'inProgress', label: 'In Progress', isPrimary: true,  dotColor: 'primary', borderColor: null      },
  { key: 'todo',       label: 'To Do',        isPrimary: false, dotColor: null,      borderColor: null      },
  { key: 'done',       label: 'Done',         isPrimary: false, dotColor: null,      borderColor: null      },
  { key: 'onHold',     label: 'On Hold',      isPrimary: false, dotColor: null,      borderColor: null      },
  { key: 'failed',     label: 'Failed',       isPrimary: false, dotColor: '#EF4444', borderColor: '#EF4444' },
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

  return (
    <>
      <Box
        ref={rowRef}
        onClick={() => setPopupOpen(p => !p)}
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: '12px',
          px: '16px',
          py: '13px',
          cursor: 'pointer',
          borderRadius: '10px',
          border: '1px solid',
          borderColor: popupOpen
            ? 'primary.main'
            : isDark ? 'rgba(255,255,255,0.07)' : 'rgba(0,0,0,0.07)',
          ...(section.borderColor && {
            borderLeft: `3px solid ${section.borderColor}`,
          }),
          ...(section.isPrimary && {
            borderLeft: `3px solid ${theme.palette.primary.main}`,
          }),
          bgcolor: 'background.paper',
          transition: 'border-color 0.15s, background-color 0.15s',
          '&:hover': {
            borderColor: popupOpen
              ? 'primary.main'
              : isDark ? 'rgba(255,255,255,0.13)' : 'rgba(0,0,0,0.13)',
            bgcolor: isDark ? 'rgba(255,255,255,0.015)' : 'rgba(0,0,0,0.01)',
          },
        }}
      >
        {/* Status dot */}
        <Box sx={{ flexShrink: 0, width: 8, height: 8, position: 'relative' }}>
          {section.dotColor && (
            <>
              {/* Solid dot */}
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
              {/* Ping ring */}
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
            color: 'text.primary',
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

        {/* GitHub link */}
        {task.url && (
          <Tooltip title="Open in GitHub">
            <IconButton
              component="a"
              href={task.url}
              target="_blank"
              rel="noopener noreferrer"
              size="small"
              onClick={(e: React.MouseEvent) => e.stopPropagation()}
              sx={{ p: '4px', color: 'text.disabled', '&:hover': { color: 'text.secondary' } }}
            >
              <Github size={14} />
            </IconButton>
          </Tooltip>
        )}

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
        mb: '6px',
        border: '1px solid',
        borderColor: 'divider',
        borderRadius: '10px !important',
        '&:before': { display: 'none' },
        overflow: 'hidden',
      }}
    >
      <AccordionSummary
        expandIcon={<ChevronDown size={14} style={{ color: labelColor }} />}
        sx={{
          minHeight: '40px',
          px: '14px',
          py: 0,
          '& .MuiAccordionSummary-content': { my: '8px', alignItems: 'center', gap: '6px' },
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

      <AccordionDetails sx={{ p: '10px', display: 'flex', flexDirection: 'column', gap: '6px' }}>
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
          <Box sx={{ py: '16px', display: 'flex', justifyContent: 'center' }}>
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
  } = useProjectBoard(orgId, projectId);

  // Derive a GitHub project board URL from the first available task URL.
  const firstTaskUrl =
    board.inProgress[0]?.url ?? board.todo[0]?.url ?? board.done[0]?.url ?? null;
  const githubProjectUrl = firstTaskUrl
    ? firstTaskUrl.replace(/\/issues\/\d+.*$/, '') + '/projects'
    : null;

  if (isLoading) {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', pt: 16, gap: 1.5 }}>
        <CircularProgress size={28} thickness={3} />
        <Typography variant="body2" color="text.disabled">Loading tasks…</Typography>
      </Box>
    );
  }

  if (error) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', pt: 16 }}>
        <Typography variant="body2" color="error.main">{error}</Typography>
      </Box>
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
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      <TasksPageHeader
        projectId={projectId ?? ''}
        totalTasks={totalTasks}
        isGenerating={isGenerating}
        isDispatching={isDispatching}
        githubProjectUrl={githubProjectUrl}
        onGenerate={handleGenerate}
        onStartImplementation={handleStartImplementation}
      />

      {actionError && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: '12px',
            px: '16px',
            py: '12px',
            mb: 2,
            borderRadius: '10px',
            bgcolor: 'rgba(239, 68, 68, 0.08)',
            border: '1px solid',
            borderColor: 'rgba(239, 68, 68, 0.2)',
          }}
        >
          <Box sx={{ flex: 1 }}>
            <Typography sx={{ fontSize: '0.875rem', color: '#EF4444', lineHeight: 1.3 }}>
              {actionError}
            </Typography>
          </Box>
          <IconButton
            size="small"
            onClick={clearActionError}
            sx={{ color: '#EF4444', p: 0.5 }}
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
            gap: '12px',
            px: '16px',
            py: '12px',
            mb: '16px',
            borderRadius: '10px',
            bgcolor: 'rgba(239, 68, 68, 0.08)',
            border: '1px solid',
            borderColor: 'rgba(239, 68, 68, 0.2)',
            cursor: 'pointer',
            transition: 'all 0.15s',
            '&:hover': { bgcolor: 'rgba(239, 68, 68, 0.12)', borderColor: 'rgba(239, 68, 68, 0.3)' },
          }}
        >
          <Box sx={{ flexShrink: 0, color: '#EF4444', display: 'flex' }}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2L1 21h22L12 2zm0 3.5L20.5 19H3.5L12 5.5zM11 10v4h2v-4h-2zm0 6v2h2v-2h-2z"/></svg>
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography sx={{ fontWeight: 700, fontSize: '0.875rem', color: '#EF4444', lineHeight: 1.3 }}>
              {failedCount} task{failedCount !== 1 ? 's' : ''} failed
            </Typography>
            <Typography sx={{ fontSize: '0.75rem', color: '#DC2626', lineHeight: 1.3 }}>
              Click to review failed tasks
            </Typography>
          </Box>
          <ChevronRight size={16} style={{ flexShrink: 0, color: '#EF4444' }} />
        </Box>
      )}

      {bannerVariant === 'in_progress' && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: '12px',
            px: '16px',
            py: '12px',
            mb: '16px',
            borderRadius: '10px',
            bgcolor: 'rgba(59, 130, 246, 0.08)',
            border: '1px solid',
            borderColor: 'rgba(59, 130, 246, 0.2)',
          }}
        >
          <Box sx={{ flexShrink: 0, display: 'flex' }}>
            <CircularProgress size={16} sx={{ color: '#3B82F6' }} />
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography sx={{ fontWeight: 700, fontSize: '0.875rem', color: '#3B82F6', lineHeight: 1.3 }}>
              {inProgressCount} task{inProgressCount !== 1 ? 's' : ''} in progress
            </Typography>
            <Typography sx={{ fontSize: '0.75rem', color: '#1F2937', lineHeight: 1.3 }}>
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
            gap: '12px',
            px: '16px',
            py: '12px',
            mb: '16px',
            borderRadius: '10px',
            bgcolor: 'rgba(34, 197, 94, 0.08)',
            border: '1px solid',
            borderColor: 'rgba(34, 197, 94, 0.2)',
          }}
        >
          <Box sx={{ flexShrink: 0, color: '#22C55E', display: 'flex' }}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41L9 16.17z"/></svg>
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography sx={{ fontWeight: 700, fontSize: '0.875rem', color: '#22C55E', lineHeight: 1.3 }}>
              All pending tasks are complete
            </Typography>
            <Typography sx={{ fontSize: '0.75rem', color: '#1F2937', lineHeight: 1.3 }}>
              All tasks have been completed successfully
            </Typography>
          </Box>
        </Box>
      )}

      {/* Task sections */}
      <Box sx={{ flex: 1, overflowY: 'auto', pr: '2px' }}>
        {totalTasks === 0 && (
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: '12px',
              px: '16px',
              py: '12px',
              mb: '16px',
              borderRadius: '10px',
              bgcolor: 'rgba(59, 130, 246, 0.08)',
              border: '1px solid',
              borderColor: 'rgba(59, 130, 246, 0.2)',
            }}
          >
            <Box sx={{ flexShrink: 0, display: 'flex' }}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" style={{ color: '#3B82F6' }}>
                <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
              </svg>
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography sx={{ fontWeight: 700, fontSize: '0.875rem', color: '#3B82F6', lineHeight: 1.3 }}>
                No tasks generated yet
              </Typography>
              <Typography sx={{ fontSize: '0.75rem', color: '#1F2937', lineHeight: 1.3 }}>
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
  );
}
