import { useRef, useState } from 'react';
import { alpha, Box, IconButton, Tooltip, Typography } from '@wso2/oxygen-ui';
import { ChevronDown, ChevronRight, Github, OctagonAlert } from '@wso2/oxygen-ui-icons-react';
import { TaskDetailPopup } from './TaskDetailPopup';
import { LabelList } from './LabelList';
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
  const rowRef = useRef<HTMLDivElement>(null);
  const [popupOpen, setPopupOpen] = useState(false);

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
            : popupOpen ? 'primary.main' : 'divider',
          ...(isFailed && { borderLeft: '3px solid', borderLeftColor: 'error.main' }),
          ...(!isFailed && section.borderColor && { borderLeft: '3px solid', borderLeftColor: section.borderColor }),
          ...(!isFailed && section.isPrimary && { borderLeft: '3px solid', borderLeftColor: 'primary.main' }),
          bgcolor: isFailed ? (t) => alpha(t.palette.error.main, 0.04) : 'background.paper',
          transition: 'border-color 0.15s, background-color 0.15s',
          '&:hover': {
            borderColor: isFailed
              ? 'error.dark'
              : popupOpen ? 'primary.main' : (t) => alpha(t.palette.text.primary, 0.13),
            bgcolor: isFailed
              ? (t) => alpha(t.palette.error.main, 0.07)
              : (t) => alpha(t.palette.text.primary, 0.01),
          },
          ...CARD_ANIMATION,
          animationDelay,
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

        {/* Title */}
        <Typography
          variant="body2"
          sx={{
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

        {/* Expand indicator */}
        <Box sx={{ flexShrink: 0, color: 'text.disabled' }}>
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
