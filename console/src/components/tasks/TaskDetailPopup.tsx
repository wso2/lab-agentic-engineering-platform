import { useState } from 'react';
import {
  Box,
  Button,
  CircularProgress,
  ClickAwayListener,
  Paper,
  Popper,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import ReactMarkdown from 'react-markdown';
import { Play } from 'lucide-react';
import { api } from '../../services/api';
import type { Task } from '../../services/api';
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
  const theme = useTheme();
  const isDark = theme.palette.mode === 'dark';
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
      style={{ zIndex: 1300 }}
    >
      <ClickAwayListener onClickAway={onClose}>
        <Paper
          elevation={4}
          sx={{
            width: 440,
            maxHeight: 520,
            p: '16px',
            borderRadius: '12px',
            border: '1px solid',
            borderColor: isDark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)',
            bgcolor: 'background.paper',
            display: 'flex',
            flexDirection: 'column',
            gap: '12px',
            overflow: 'hidden',
          }}
        >
          <Typography sx={{ fontSize: '0.8125rem', fontWeight: 600, lineHeight: 1.4, color: 'text.primary' }}>
            {task.title}
          </Typography>

          {task.description && (
            <Box
              sx={{
                overflowY: 'auto',
                maxHeight: 300,
                pr: '4px',
                '&::-webkit-scrollbar': { width: 4 },
                '&::-webkit-scrollbar-track': { bgcolor: 'transparent' },
                '&::-webkit-scrollbar-thumb': {
                  bgcolor: isDark ? 'rgba(255,255,255,0.15)' : 'rgba(0,0,0,0.15)',
                  borderRadius: '4px',
                },
                '& .md-body': {
                  fontSize: '0.75rem',
                  color: 'text.secondary',
                  lineHeight: 1.65,
                  '& h1, & h2, & h3, & h4': {
                    fontSize: '0.8125rem',
                    fontWeight: 600,
                    color: 'text.primary',
                    mt: '10px',
                    mb: '4px',
                  },
                  '& p': { m: 0, mb: '6px' },
                  '& ul, & ol': { pl: '18px', m: 0, mb: '6px' },
                  '& li': { mb: '2px' },
                  '& code': {
                    fontFamily: 'monospace',
                    fontSize: '0.7rem',
                    bgcolor: isDark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)',
                    px: '4px',
                    py: '1px',
                    borderRadius: '3px',
                  },
                  '& pre': {
                    bgcolor: isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)',
                    p: '8px',
                    borderRadius: '6px',
                    overflowX: 'auto',
                    mb: '6px',
                    '& code': { bgcolor: 'transparent', p: 0 },
                  },
                  '& strong': { fontWeight: 600, color: 'text.primary' },
                  '& a': { color: 'primary.main' },
                  '& blockquote': {
                    borderLeft: '3px solid',
                    borderColor: isDark ? 'rgba(255,255,255,0.15)' : 'rgba(0,0,0,0.15)',
                    pl: '10px',
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

          <Box sx={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
            <Typography sx={{ fontSize: '0.7rem', color: 'text.disabled', fontWeight: 500 }}>Assignee</Typography>
            <AssigneeChip assignee={task.assignee} />
          </Box>

          {task.labels && task.labels.length > 0 && (
            <Box>
              <Typography sx={{ fontSize: '0.7rem', color: 'text.disabled', fontWeight: 500, mb: '6px' }}>Labels</Typography>
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
        </Paper>
      </ClickAwayListener>
    </Popper>
  );
}
