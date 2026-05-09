import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  alpha,
  Box,
  Button,
  CircularProgress,
  ClickAwayListener,
  Paper,
  Popper,
  Typography,
} from '@wso2/oxygen-ui';
import ReactMarkdown from 'react-markdown';
import { ChevronRight, Play } from '@wso2/oxygen-ui-icons-react';
import { api } from '../../services/api';
import type { Task } from '../../services/api';
import { projectTaskDetailPath } from '../../lib/paths';
import { AssigneeChip } from './AssigneeChip';
import { LabelList } from './LabelList';

interface TaskDetailPopupProps {
  open: boolean;
  anchorEl: HTMLElement | null;
  task: Task;
  isInTodo: boolean;
  orgId: string;
  projectId: string;
  onClose: () => void;
}

export function TaskDetailPopup({ open, anchorEl, task, isInTodo, orgId, projectId, onClose }: TaskDetailPopupProps) {
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
    <Popper
      open={open}
      anchorEl={anchorEl}
      placement="right-start"
      modifiers={[{ name: 'offset', options: { offset: [0, 8] } }]}
      sx={{ zIndex: 'modal' }}
    >
      <ClickAwayListener onClickAway={onClose}>
        <Paper
          elevation={4}
          sx={{
            width: 440,
            maxHeight: 520,
            p: 2,
            borderRadius: 1.5,
            border: '1px solid',
            borderColor: 'divider',
            bgcolor: 'background.paper',
            display: 'flex',
            flexDirection: 'column',
            gap: 1.5,
            overflow: 'hidden',
          }}
        >
          <Typography variant="body2" sx={{ fontWeight: 600, lineHeight: 1.4, color: 'text.primary' }}>
            {task.title}
          </Typography>

          {task.description && (
            <Box
              sx={{
                overflowY: 'auto',
                maxHeight: 300,
                pr: 0.5,
                '&::-webkit-scrollbar': { width: 4 },
                '&::-webkit-scrollbar-track': { bgcolor: 'transparent' },
                '&::-webkit-scrollbar-thumb': {
                  bgcolor: (t) => alpha(t.palette.text.primary, 0.15),
                  borderRadius: 0.5,
                },
                '& .md-body': {
                  fontSize: '0.75rem',
                  color: 'text.secondary',
                  lineHeight: 1.65,
                  '& h1, & h2, & h3, & h4': {
                    fontSize: '0.8125rem',
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
                    fontSize: '0.7rem',
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

          <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75 }}>
            <Typography variant="caption" sx={{ color: 'text.disabled', fontWeight: 500 }}>Assignee</Typography>
            <AssigneeChip assignee={task.assignee} />
          </Box>

          {task.labels && task.labels.length > 0 && (
            <Box>
              <Typography variant="caption" sx={{ color: 'text.disabled', fontWeight: 500, mb: 0.75, display: 'block' }}>Labels</Typography>
              <LabelList labels={task.labels} />
            </Box>
          )}

          {isInTodo && task.componentTaskId && (
            <Button
              variant="contained"
              size="small"
              startIcon={isExecuting ? <CircularProgress size={12} color="inherit" /> : <Play size={12} />}
              disabled={isExecuting}
              onClick={handleExecute}
              sx={{ alignSelf: 'flex-start', mt: 'auto' }}
            >
              {isExecuting ? 'Executing…' : 'Execute Now'}
            </Button>
          )}

          {!isInTodo && task.componentTaskId && (
            <Button
              component={RouterLink}
              to={projectTaskDetailPath(orgId, projectId, task.componentTaskId)}
              variant="outlined"
              size="small"
              endIcon={<ChevronRight size={12} />}
              onClick={(e: React.MouseEvent) => { e.stopPropagation(); onClose(); }}
              sx={{ alignSelf: 'flex-start', mt: 'auto' }}
            >
              View execution progress
            </Button>
          )}
        </Paper>
      </ClickAwayListener>
    </Popper>
  );
}
