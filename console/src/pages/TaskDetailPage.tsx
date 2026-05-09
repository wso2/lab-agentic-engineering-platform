import { useMemo } from 'react';
import { Link as RouterLink, useParams } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Divider,
  PageContent,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { ChevronLeft } from '@wso2/oxygen-ui-icons-react';
import { useTaskStatus } from '../hooks/useTaskStatus';
import { useTaskAgentProgress } from '../hooks/useTaskAgentProgress';
import { useTaskBuildProgress } from '../hooks/useTaskBuildProgress';
import { TaskPipelineStrip } from '../components/tasks/TaskPipelineStrip';
import { TaskActivityFeed } from '../components/tasks/TaskActivityFeed';
import { TaskArtifactsBar } from '../components/tasks/TaskArtifactsBar';
import { projectTasksPath } from '../lib/paths';
import type { TaskStatus } from '../services/api';

const STATUS_TONE: Record<TaskStatus, 'default' | 'primary' | 'success' | 'error' | 'warning'> = {
  pending: 'default',
  pending_deps: 'default',
  in_progress: 'primary',
  ready_for_review: 'primary',
  merged: 'success',
  building: 'primary',
  deployed: 'success',
  rejected: 'warning',
  failed: 'error',
  abandoned: 'default',
};

function formatElapsed(ms: number): string {
  if (ms < 0) ms = 0;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

const STUCK_THRESHOLD_MS = 30 * 60 * 1000; // 30 minutes

function emptyMessageFor(status: TaskStatus | undefined, dispatchedAt: string | undefined, runName: string | undefined): string {
  if (status === 'pending' || status === 'pending_deps') return 'Waiting for the agent to start…';
  if (status === 'in_progress') {
    // Detect a likely-stuck task: in_progress for >30 min with no agent run recorded.
    if (dispatchedAt && !runName) {
      const age = Date.now() - new Date(dispatchedAt).getTime();
      if (age > STUCK_THRESHOLD_MS) {
        return 'Task appears stuck — no agent run recorded. Try re-dispatching from the tasks page.';
      }
    }
    return 'Agent has started. Streaming activity will appear here as the agent works…';
  }
  if (status === 'building') return 'Build dispatched. Waiting for build steps…';
  return 'No activity recorded for this task.';
}

export default function TaskDetailPage() {
  const { orgId, projectId, taskId } = useParams<{ orgId: string; projectId: string; taskId: string }>();
  const status = useTaskStatus(orgId, projectId, taskId);
  const task = status.data?.task;
  const taskStatus = task?.status as TaskStatus | undefined;

  const agent = useTaskAgentProgress(orgId, projectId, taskId, taskStatus);
  const build = useTaskBuildProgress(orgId, projectId, taskId, taskStatus);

  const elapsed = useMemo(() => {
    if (!task?.dispatchedAt) return null;
    const start = new Date(task.dispatchedAt).getTime();
    if (isNaN(start)) return null;
    return formatElapsed(Date.now() - start);
  }, [task?.dispatchedAt, status.dataUpdatedAt]);

  if (status.isLoading) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', pt: 16, gap: 1.5 }}>
          <CircularProgress size={28} thickness={3} />
          <Typography variant="body2" color="text.disabled">Loading task…</Typography>
        </Box>
      </PageContent>
    );
  }

  if (status.error || !task) {
    return (
      <PageContent>
        <Box sx={{ pt: 8, textAlign: 'center' }}>
          <Typography variant="body2" color="error.main">
            {(status.error as Error | undefined)?.message ?? 'Task not found.'}
          </Typography>
          <Button
            component={RouterLink}
            to={projectTasksPath(orgId ?? '', projectId ?? '')}
            startIcon={<ChevronLeft size={14} />}
            size="small"
            sx={{ mt: 2 }}
          >
            Back to tasks
          </Button>
        </Box>
      </PageContent>
    );
  }

  return (
    <PageContent sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Stack spacing={2} sx={{ pb: 2 }}>
        <Stack direction="row" spacing={1.5} alignItems="center">
          <Button
            component={RouterLink}
            to={projectTasksPath(orgId ?? '', projectId ?? '')}
            startIcon={<ChevronLeft size={14} />}
            size="small"
            variant="text"
            sx={{ minWidth: 0, color: 'text.secondary' }}
          >
            Tasks
          </Button>
          <Typography variant="h6" sx={{ flex: 1, fontWeight: 600 }}>
            {task.title || task.componentName || 'Task'}
          </Typography>
          <Chip
            label={task.status.replace(/_/g, ' ')}
            color={STATUS_TONE[task.status as TaskStatus] ?? 'default'}
            size="small"
            sx={{ textTransform: 'uppercase', fontSize: '0.7rem', fontWeight: 700 }}
          />
          {elapsed && (
            <Typography variant="caption" color="text.disabled" sx={{ minWidth: 70, textAlign: 'right' }}>
              {elapsed}
            </Typography>
          )}
        </Stack>

        <Card variant="outlined">
          <CardContent>
            <TaskPipelineStrip status={taskStatus} />
          </CardContent>
        </Card>

        <Card variant="outlined">
          <CardContent>
            <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 0.5 }}>
              <Typography variant="overline" sx={{ color: 'text.disabled' }}>Activity</Typography>
              {taskStatus === 'in_progress' && agent.lines.length > 0 && !agent.final && (
                <Box sx={{
                  width: 6, height: 6, borderRadius: '50%',
                  bgcolor: 'success.main',
                  animation: 'pulse 1.4s ease-out infinite',
                  '@keyframes pulse': {
                    '0%':   { opacity: 1 },
                    '100%': { opacity: 0.2 },
                  },
                }} />
              )}
              {taskStatus === 'in_progress' && agent.lines.length > 0 && (
                <Typography variant="caption" color="text.disabled">
                  · live
                </Typography>
              )}
            </Stack>
            <TaskActivityFeed
              agentLines={agent.lines}
              buildLines={build.lines}
              agentFinal={agent.final}
              buildFinal={build.final}
              emptyMessage={emptyMessageFor(taskStatus, task.dispatchedAt, task.lastCodingAgentRunName)}
            />
            {(agent.error || build.error) && (
              <Box sx={{ mt: 1.5 }}>
                <Typography variant="caption" color="warning.main">
                  Live progress unavailable — falling back to status polling. Pipeline + status remain accurate.
                </Typography>
              </Box>
            )}
          </CardContent>
        </Card>

        <Card variant="outlined">
          <CardContent>
            <Typography variant="overline" sx={{ color: 'text.disabled' }}>Artifacts</Typography>
            <Box sx={{ mt: 0.5 }}>
              <TaskArtifactsBar task={task} />
            </Box>
            {status.data?.buildSteps && status.data.buildSteps.length > 0 && (
              <>
                <Divider sx={{ my: 1.5 }} />
                <Typography variant="overline" sx={{ color: 'text.disabled' }}>Build steps</Typography>
                <Stack spacing={0.5} sx={{ mt: 0.5 }}>
                  {status.data.buildSteps.map((s, i) => (
                    <Typography key={`${s.name}-${i}`} variant="caption" color="text.secondary">
                      {s.name}{s.phase ? ` · ${s.phase}` : ''}{s.message ? ` · ${s.message}` : ''}
                    </Typography>
                  ))}
                </Stack>
              </>
            )}
          </CardContent>
        </Card>
      </Stack>
    </PageContent>
  );
}
