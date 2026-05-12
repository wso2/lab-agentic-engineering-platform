import { useEffect, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { alpha, Box, Button, Typography, useTheme } from '@wso2/oxygen-ui';
import { ArrowUpRight } from '@wso2/oxygen-ui-icons-react';
import type { TaskStatus } from '../../services/api';
import { projectTaskDetailPath } from '../../lib/paths';
import { formatElapsedSince } from '../../lib/relativeTime';

interface TaskStatusInlineProps {
  status: TaskStatus | undefined;
  dispatchedAt: string | undefined;
  componentTaskId: string | undefined;
  orgId: string;
  projectId: string;
}

const NON_TERMINAL: TaskStatus[] = [
  'pending',
  'pending_deps',
  'in_progress',
  'ready_for_review',
  'merged',
  'building',
];

const STATUS_DISPLAY: Record<TaskStatus, { label: string; tone: 'primary' | 'success' | 'warning' | 'error' | 'muted' }> = {
  pending:          { label: 'Pending',           tone: 'muted'   },
  pending_deps:     { label: 'Waiting on deps',   tone: 'muted'   },
  in_progress:      { label: 'In Progress',       tone: 'primary' },
  ready_for_review: { label: 'Awaiting review',   tone: 'primary' },
  merged:           { label: 'Merged',            tone: 'primary' },
  building:         { label: 'Building',          tone: 'primary' },
  deployed:         { label: 'Deployed',          tone: 'success' },
  rejected:         { label: 'Rejected',          tone: 'warning' },
  failed:           { label: 'Failed',            tone: 'error'   },
  abandoned:        { label: 'Abandoned',         tone: 'muted'   },
};

// useElapsedTick re-renders every `intervalMs` while `active` is true, so
// the elapsed-time caption stays current without a server round-trip.
function useElapsedTick(active: boolean, intervalMs = 30_000) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setTick(t => t + 1), intervalMs);
    return () => clearInterval(id);
  }, [active, intervalMs]);
}

export function TaskStatusInline({ status, dispatchedAt, componentTaskId, orgId, projectId }: TaskStatusInlineProps) {
  const theme = useTheme();
  const isLive = !!status && NON_TERMINAL.includes(status) && status !== 'pending' && status !== 'pending_deps';
  useElapsedTick(isLive);

  // Hide entirely for fresh, never-dispatched tasks — the row stays clean
  // and matches its pre-execution look.
  if (!componentTaskId || !status || (status === 'pending' && !dispatchedAt)) {
    return null;
  }

  const display = STATUS_DISPLAY[status];
  const tone =
    display.tone === 'primary' ? theme.palette.primary.main
    : display.tone === 'success' ? theme.palette.success.main
    : display.tone === 'warning' ? theme.palette.warning.main
    : display.tone === 'error' ? theme.palette.error.main
    : theme.palette.text.disabled;

  const elapsed = formatElapsedSince(dispatchedAt);
  const caption = elapsed
    ? (isLive ? `started ${elapsed} ago` : `${elapsed} ago`)
    : null;

  // Only show the Live progress affordance once the task has actually
  // dispatched — there's nothing to view otherwise.
  const showLiveButton = !!dispatchedAt;

  return (
    <Box
      sx={{
        display: 'flex',
        alignItems: 'center',
        gap: 1,
        flexShrink: 0,
        minWidth: 0,
      }}
      onClick={(e) => e.stopPropagation()}
    >
      {/* Status dot + label */}
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.625, minWidth: 0 }}>
        <Box
          sx={{
            width: 8,
            height: 8,
            borderRadius: '50%',
            bgcolor: tone,
            flexShrink: 0,
            position: 'relative',
            ...(isLive && {
              '&::after': {
                content: '""',
                position: 'absolute',
                inset: -3,
                borderRadius: '50%',
                bgcolor: tone,
                opacity: 0.45,
                animation: 'taskStatusPulse 1.4s ease-out infinite',
              },
              '@keyframes taskStatusPulse': {
                '0%':   { transform: 'scale(0.8)', opacity: 0.5 },
                '100%': { transform: 'scale(2.2)', opacity: 0   },
              },
            }),
          }}
        />
        <Typography
          variant="caption"
          sx={{
            fontWeight: 600,
            color: tone,
            whiteSpace: 'nowrap',
            fontSize: '0.72rem',
          }}
        >
          {display.label}
        </Typography>
        {caption && (
          <Typography
            variant="caption"
            sx={{
              color: 'text.disabled',
              whiteSpace: 'nowrap',
              fontSize: '0.7rem',
            }}
          >
            · {caption}
          </Typography>
        )}
      </Box>

      {/* Live progress button — primary + pulsing while non-terminal,
          outlined otherwise. Animates in on first appearance. */}
      {showLiveButton && (
        <Button
          component={RouterLink}
          to={projectTaskDetailPath(orgId, projectId, componentTaskId)}
          variant={isLive ? 'contained' : 'outlined'}
          size="small"
          endIcon={<ArrowUpRight size={12} />}
          sx={{
            flexShrink: 0,
            minWidth: 0,
            px: 1.25,
            py: 0.25,
            fontSize: '0.7rem',
            fontWeight: 600,
            textTransform: 'none',
            animation: 'taskLiveBtnIn 0.32s ease both',
            '@keyframes taskLiveBtnIn': {
              from: { opacity: 0, transform: 'translateX(6px) scale(0.92)' },
              to:   { opacity: 1, transform: 'translateX(0)   scale(1)'    },
            },
            ...(isLive && {
              animationName: 'taskLiveBtnIn, taskLiveBtnPulse',
              animationDuration: '0.32s, 1.8s',
              animationTimingFunction: 'ease, ease-out',
              animationIterationCount: '1, infinite',
              animationDelay: '0s, 0.32s',
              animationFillMode: 'both, none',
              '@keyframes taskLiveBtnPulse': {
                '0%':   { boxShadow: `0 0 0 0   ${alpha(theme.palette.primary.main, 0.45)}` },
                '70%':  { boxShadow: `0 0 0 6px ${alpha(theme.palette.primary.main, 0   )}` },
                '100%': { boxShadow: `0 0 0 0   ${alpha(theme.palette.primary.main, 0   )}` },
              },
            }),
          }}
        >
          {isLive ? 'Live progress' : 'View progress'}
        </Button>
      )}
    </Box>
  );
}
