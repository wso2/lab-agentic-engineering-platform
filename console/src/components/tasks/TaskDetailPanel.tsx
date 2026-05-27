import { useState } from 'react';
import {
  alpha,
  Box,
  Button,
  CircularProgress,
  Tooltip,
  Typography,
} from '@wso2/oxygen-ui';
import ReactMarkdown from 'react-markdown';
import { Play, RotateCcw } from '@wso2/oxygen-ui-icons-react';
import { api } from '../../services/api';
import type { Task } from '../../services/api';
import { AssigneeChip } from './AssigneeChip';

interface TaskDetailPanelProps {
  task: Task;
  orgId: string;
  projectId: string;
  onClose: () => void;
}

export function TaskDetailPanel({ task, orgId, projectId, onClose }: TaskDetailPanelProps) {
  const [isExecuting, setIsExecuting] = useState(false);
  const [isRetrying, setIsRetrying] = useState(false);

  const handleExecute = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!task.componentTaskId) return;
    setIsExecuting(true);
    try {
      await api.execTask(orgId, projectId, task.componentTaskId);
    } catch {
      // silently fail — user can retry
    } finally {
      setIsExecuting(false);
      onClose();
    }
  };

  const handleRetry = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!task.componentTaskId) return;
    setIsRetrying(true);
    try {
      await api.retryTask(orgId, projectId, task.componentTaskId);
    } catch {
      // silently fail — board polling will refresh state
    } finally {
      setIsRetrying(false);
      onClose();
    }
  };

  return (
    <Box
      onClick={(e) => e.stopPropagation()}
      sx={{
        borderTop: '1px solid',
        borderColor: 'divider',
        px: 2,
        py: 1.75,
        display: 'flex',
        flexDirection: 'column',
        gap: 1.5,
        bgcolor: (t) => alpha(t.palette.text.primary, 0.015),
      }}
    >
      {task.description && (
        <Box
          sx={{
            maxHeight: 360,
            overflowY: 'auto',
            pr: 0.5,
            '&::-webkit-scrollbar': { width: 4 },
            '&::-webkit-scrollbar-track': { bgcolor: 'transparent' },
            '&::-webkit-scrollbar-thumb': {
              bgcolor: (t) => alpha(t.palette.text.primary, 0.15),
              borderRadius: 0.5,
            },
            '& .md-body': {
              fontSize: '0.78rem',
              color: 'text.secondary',
              lineHeight: 1.65,
              '& h1, & h2, & h3, & h4': {
                fontSize: '0.84rem',
                fontWeight: 600,
                color: 'text.primary',
                mt: 1.25,
                mb: 0.5,
              },
              '& p': { m: 0, mb: 0.75 },
              '& ul, & ol': { pl: 2.25, m: 0, mb: 0.75 },
              '& li': { mb: 0.25 },
              '& code': {
                fontFamily: 'monospace',
                fontSize: '0.72rem',
                bgcolor: (t) => alpha(t.palette.text.primary, 0.06),
                px: 0.5,
                py: 0.125,
                borderRadius: 0.75,
              },
              '& pre': {
                bgcolor: (t) => alpha(t.palette.text.primary, 0.04),
                p: 1,
                borderRadius: 1.5,
                overflowX: 'auto',
                mb: 0.75,
                '& code': { bgcolor: 'transparent', p: 0 },
              },
              '& strong': { fontWeight: 600, color: 'text.primary' },
              '& a': { color: 'primary.main' },
              '& blockquote': {
                borderLeft: '3px solid',
                borderColor: 'divider',
                pl: 1.25,
                ml: 0,
                color: 'text.disabled',
              },
            },
          }}
        >
          <Box className="md-body">
            <ReactMarkdown>{task.description}</ReactMarkdown>
          </Box>
        </Box>
      )}

      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: 1.5,
        }}
      >
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75 }}>
          <Typography variant="caption" sx={{ color: 'text.disabled', fontWeight: 500 }}>Assignee</Typography>
          <AssigneeChip assignee={task.assignee} />
        </Box>

        <Box sx={{ flex: 1 }} />

        {/* F3c — Retry surface for terminal failure states. Re-dispatches the
            task with a fresh run + freshly minted bearer; the agent pushes
            new commits to the same draft PR.
            - `verification_failed`: tech-lead review caught an issue.
            - `failed`: coding-agent pod itself errored (new-path Job
              terminal Failed, e.g. missing env, image pull, OOM). Both
              transitions go through dispatchSvc.RetryTask which clears
              LastCodingAgentRunName + DispatchedAt so the next dispatch
              creates a new Job. */}
        {(task.status === 'verification_failed' || task.status === 'failed') && task.componentTaskId && (
          <Button
            variant="contained"
            size="small"
            color="warning"
            startIcon={isRetrying ? <CircularProgress size={12} color="inherit" /> : <RotateCcw size={12} />}
            disabled={isRetrying}
            onClick={handleRetry}
          >
            {isRetrying ? 'Retrying…' : 'Retry'}
          </Button>
        )}

        {/* Execute Now only fires the per-task /tasks/{id}/exec endpoint,
            which does meaningful work only for SYSTEM tasks (DB / infra
            provisioning). WORKER (coding-agent) tasks must go through the
            batch dispatch path — hide the button for them so users don't
            see a no-op affordance. Pre-dispatch states only; once
            dispatched, the row's Live progress button is the primary
            affordance. */}
        {task.componentTaskId && task.execType === 'SYSTEM' && (!task.status || task.status === 'pending' || task.status === 'on_hold') && (
          task.status === 'on_hold' ? (
            <Tooltip title="Waiting on prerequisite tasks to complete">
              <span>
                <Button
                  variant="contained"
                  size="small"
                  startIcon={<Play size={12} />}
                  disabled
                >
                  Execute Now
                </Button>
              </span>
            </Tooltip>
          ) : (
            <Button
              variant="contained"
              size="small"
              startIcon={isExecuting ? <CircularProgress size={12} color="inherit" /> : <Play size={12} />}
              disabled={isExecuting}
              onClick={handleExecute}
            >
              {isExecuting ? 'Executing…' : 'Execute Now'}
            </Button>
          )
        )}
      </Box>
    </Box>
  );
}
