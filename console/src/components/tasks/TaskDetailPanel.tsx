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
import { Play } from '@wso2/oxygen-ui-icons-react';
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

        {/* Execute Now is only available pre-dispatch. Once dispatched,
            the row carries a Live progress button as its primary
            affordance — no buttons inside the panel. */}
        {task.componentTaskId && (!task.status || task.status === 'pending' || task.status === 'pending_deps') && (
          task.status === 'pending_deps' ? (
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
