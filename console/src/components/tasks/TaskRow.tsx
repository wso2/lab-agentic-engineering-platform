import { useState } from 'react';
import { alpha, Box, Collapse, IconButton, Tooltip, Typography } from '@wso2/oxygen-ui';
import { ChevronDown, ChevronRight, Github, OctagonAlert } from '@wso2/oxygen-ui-icons-react';
import { TaskDetailPanel } from './TaskDetailPanel';
import { LabelList } from './LabelList';
import { TaskStatusInline } from './TaskStatusInline';
import type { Task } from '../../services/api';
import type { SectionConfig } from './types';

interface TaskRowProps {
  task: Task;
  section: SectionConfig;
  orgId: string;
  projectId: string;
  index: number;
}

const CARD_ANIMATION = {
  animation: 'taskFadeIn 0.22s ease both',
  '@keyframes taskFadeIn': {
    from: { opacity: 0, transform: 'translateY(5px)' },
    to:   { opacity: 1, transform: 'translateY(0)' },
  },
} as const;

export function TaskRow({ task, section, orgId, projectId, index }: TaskRowProps) {
  const [expanded, setExpanded] = useState(false);

  const lifecycle = task.lifecycleStatus ?? 'gh_issue_created';
  const isWaiting = lifecycle === 'gh_issue_waiting';
  const isSyncing = lifecycle === 'gh_issue_syncing';
  const isFailed  = lifecycle === 'gh_issue_failed';

  const animationDelay = `${index * 0.045}s`;

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
          borderColor: 'divider',
          bgcolor: 'background.paper',
          opacity: 0.55,
          ...CARD_ANIMATION,
          animationDelay,
          '@keyframes taskFadeIn': {
            from: { opacity: 0, transform: 'translateY(5px)' },
            to:   { opacity: 0.55, transform: 'translateY(0)' },
          },
        }}
      >
        {/* Pulsing dot */}
        <Box sx={{ flexShrink: 0, width: 8, height: 8, position: 'relative' }}>
          <Box sx={{ width: 8, height: 8, borderRadius: '50%', bgcolor: 'text.disabled', position: 'relative', zIndex: 1 }} />
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

        <Typography variant="body2" sx={{ flex: 1, fontWeight: 450, color: 'text.disabled', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontStyle: 'italic' }}>
          Task created — syncing with project board…
        </Typography>
      </Box>
    );
  }

  return (
    <Box
      sx={{
        borderRadius: 1.25,
        border: '1px solid',
        borderColor: isFailed
          ? 'error.main'
          : expanded ? 'primary.main' : 'divider',
        ...(isFailed && { borderLeft: '3px solid', borderLeftColor: 'error.main' }),
        ...(!isFailed && section.borderColor && { borderLeft: '3px solid', borderLeftColor: section.borderColor }),
        ...(!isFailed && section.isPrimary && { borderLeft: '3px solid', borderLeftColor: 'primary.main' }),
        bgcolor: isFailed ? (t) => alpha(t.palette.error.main, 0.04) : 'background.paper',
        overflow: 'hidden',
        transition: 'border-color 0.15s, background-color 0.15s, box-shadow 0.15s',
        boxShadow: expanded ? (t) => `0 1px 3px ${alpha(t.palette.text.primary, 0.06)}` : 'none',
        '&:hover': {
          borderColor: isFailed
            ? 'error.dark'
            : expanded ? 'primary.main' : (t) => alpha(t.palette.text.primary, 0.13),
        },
        ...CARD_ANIMATION,
        animationDelay,
      }}
    >
      <Box
        onClick={() => setExpanded(p => !p)}
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 1.5,
          px: 2,
          py: 1.5,
          cursor: 'pointer',
          transition: 'background-color 0.15s',
          '&:hover': {
            bgcolor: isFailed
              ? (t) => alpha(t.palette.error.main, 0.07)
              : (t) => alpha(t.palette.text.primary, 0.02),
          },
        }}
      >
        {/* Status dot */}
        <Box sx={{ flexShrink: 0, width: 8, height: 8, position: 'relative' }}>
          {section.dotColor && !isFailed && (
            <>
              <Box sx={{ width: 8, height: 8, borderRadius: '50%', bgcolor: section.isPrimary ? 'primary.main' : section.dotColor, position: 'relative', zIndex: 1 }} />
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
                      '0%':   { transform: 'scale(1)',   opacity: 0.6 },
                      '100%': { transform: 'scale(2.8)', opacity: 0   },
                    },
                  }}
                />
              )}
            </>
          )}
        </Box>

        {/* Title (+ F4 "Waiting on" subline for pending_deps tasks, +
            F3c diagnostic for verification_failed tasks) */}
        <Box sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          <Typography
            variant="body2"
            sx={{
              fontWeight: 450,
              color: isFailed ? 'error.main' : 'text.primary',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
              minWidth: 0,
            }}
          >
            {task.title}
          </Typography>
          {task.status === 'pending_deps' && task.dependsOnComponents && task.dependsOnComponents.length > 0 && (
            <Typography
              variant="caption"
              sx={{
                color: 'warning.main',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              Waiting for: {task.dependsOnComponents.join(', ')}
            </Typography>
          )}
          {task.status === 'verification_failed' && task.errorMessage && (
            <Typography
              variant="caption"
              sx={{
                color: 'error.main',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                fontStyle: 'italic',
              }}
            >
              Verification failed — {task.errorMessage}
            </Typography>
          )}
        </Box>

        {/* Inline execution status + Live progress button — only shows
            once the task has been dispatched. */}
        <TaskStatusInline
          status={task.status}
          dispatchedAt={task.dispatchedAt}
          componentTaskId={task.componentTaskId}
          orgId={orgId}
          projectId={projectId}
        />

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

        {/* Expand indicator */}
        <Box sx={{ flexShrink: 0, color: 'text.disabled', display: 'flex' }}>
          {expanded ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </Box>
      </Box>

      <Collapse in={expanded} timeout={220} unmountOnExit>
        <TaskDetailPanel
          task={task}
          orgId={orgId}
          projectId={projectId}
          onClose={() => setExpanded(false)}
        />
      </Collapse>
    </Box>
  );
}
