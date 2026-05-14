import { Box, Stack, Typography, useTheme } from '@wso2/oxygen-ui';
import type { TaskStatus } from '../../services/api';

// Pipeline cells per task-execution-progress.md §7. The cell that maps
// to the current task status is highlighted; cells before it are marked
// done; cells after are pending.
const STAGES: { key: TaskStatus | 'dispatched'; label: string }[] = [
  { key: 'dispatched',       label: 'Dispatched' },
  { key: 'in_progress',      label: 'In Progress' },
  { key: 'ready_for_review', label: 'Ready' },
  { key: 'merged',           label: 'Merged' },
  { key: 'building',         label: 'Building' },
  { key: 'deployed',         label: 'Deployed' },
];

const FAILURE_STATUSES: TaskStatus[] = ['rejected', 'failed', 'abandoned'];

function stageIndex(status: TaskStatus | undefined): number {
  if (!status) return 0;
  if (status === 'pending' || status === 'on_hold') return 0;
  if (status === 'in_progress') return 1;
  if (status === 'ready_for_review') return 2;
  if (status === 'merged') return 3;
  if (status === 'building') return 4;
  if (status === 'deployed') return 5;
  // Failure statuses freeze at the most recent observable stage.
  return -1;
}

export function TaskPipelineStrip({ status }: { status: TaskStatus | undefined }) {
  const theme = useTheme();
  const failed = status && FAILURE_STATUSES.includes(status);
  const idx = stageIndex(status);

  return (
    <Stack direction="row" spacing={0.75} alignItems="center" sx={{ py: 1 }}>
      {STAGES.map((stage, i) => {
        const isCurrent = i === idx;
        const isDone = i < idx;
        const tone = failed
          ? theme.palette.error.main
          : isCurrent
            ? theme.palette.primary.main
            : isDone
              ? theme.palette.success.main
              : theme.palette.text.disabled;

        return (
          <Box
            key={stage.key}
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1,
              flex: 1,
              minWidth: 0,
            }}
          >
            <Box
              sx={{
                width: 8,
                height: 8,
                flexShrink: 0,
                borderRadius: '50%',
                bgcolor: tone,
                position: 'relative',
                ...(isCurrent && !failed && {
                  '&::after': {
                    content: '""',
                    position: 'absolute',
                    inset: -4,
                    borderRadius: '50%',
                    bgcolor: tone,
                    opacity: 0.4,
                    animation: 'taskPulse 1.4s ease-out infinite',
                  },
                  '@keyframes taskPulse': {
                    '0%':   { transform: 'scale(0.8)', opacity: 0.5 },
                    '100%': { transform: 'scale(2.2)', opacity: 0 },
                  },
                }),
              }}
            />
            <Typography
              sx={{
                fontSize: '0.72rem',
                fontWeight: isCurrent ? 700 : 500,
                color: tone,
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                textTransform: 'uppercase',
                letterSpacing: '0.04em',
              }}
            >
              {stage.label}
            </Typography>
          </Box>
        );
      })}
    </Stack>
  );
}
