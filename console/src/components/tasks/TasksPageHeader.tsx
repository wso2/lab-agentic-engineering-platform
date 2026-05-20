import { useState } from 'react';
import { Link as RouterLink, useParams } from 'react-router-dom';
import {
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Menu,
  MenuItem,
  Stack,
  Tooltip,
  Typography,
} from '@wso2/oxygen-ui';
import { AlertCircle, ChevronDown, Cloud, ExternalLink, Github, Laptop, Play, Sparkles } from '@wso2/oxygen-ui-icons-react';
import { useOrgAnthropic } from '../../hooks/useOrgAnthropic';

interface TasksPageHeaderProps {
  projectId: string;
  totalTasks: number;
  isGenerating: boolean;
  isDispatching: boolean;
  githubProjectUrl: string | null;
  hideGenerateButton: boolean;
  onGenerate: () => void;
  onStartImplementation: () => void;
}

export function TasksPageHeader({
  projectId,
  totalTasks,
  isGenerating,
  isDispatching,
  githubProjectUrl,
  hideGenerateButton,
  onGenerate,
  onStartImplementation,
}: TasksPageHeaderProps) {
  const [implMenuAnchor, setImplMenuAnchor] = useState<HTMLElement | null>(null);
  const [showLocalGuide, setShowLocalGuide] = useState(false);
  const { orgId } = useParams();
  const { data: anthropicProj } = useOrgAnthropic(orgId);
  const anthropicReady = anthropicProj?.status === 'active';
  const settingsUrl = `/organizations/${orgId ?? 'default'}/settings/anthropic`;

  const handleRemoteImplementation = () => {
    setImplMenuAnchor(null);
    onStartImplementation();
  };

  const handleLocalImplementation = () => {
    setImplMenuAnchor(null);
    setShowLocalGuide(true);
  };

  return (
    <>
      <Stack direction="row" alignItems="flex-start" justifyContent="space-between" sx={{ mb: 3 }}>
        <Box>
          <Typography variant="h5" fontWeight={700} sx={{ letterSpacing: '-0.02em', mb: 0.25 }}>
            Tasks
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Fetched from GitHub Project · {projectId}
          </Typography>
        </Box>

        <Stack direction="row" spacing={1} alignItems="center">
          {!hideGenerateButton && (
            <Button
              variant={totalTasks === 0 ? 'contained' : 'outlined'}
              size="small"
              startIcon={isGenerating ? <CircularProgress size={14} color="inherit" /> : <Sparkles size={15} />}
              disabled={isGenerating}
              onClick={onGenerate}
            >
              {isGenerating ? 'Generating…' : 'Generate Tasks'}
            </Button>
          )}

          {totalTasks > 0 && (
            <>
              <Button
                variant="contained"
                size="small"
                startIcon={isDispatching ? <CircularProgress size={14} color="inherit" /> : <Play size={14} />}
                endIcon={!isDispatching && <ChevronDown size={14} />}
                disabled={isDispatching}
                onClick={(e) => setImplMenuAnchor(e.currentTarget)}
                aria-haspopup="menu"
                aria-expanded={Boolean(implMenuAnchor)}
              >
                {isDispatching ? 'Starting…' : 'Execute all'}
              </Button>
              <Menu
                anchorEl={implMenuAnchor}
                open={Boolean(implMenuAnchor)}
                onClose={() => setImplMenuAnchor(null)}
                anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
                transformOrigin={{ vertical: 'top', horizontal: 'right' }}
              >
                <Tooltip
                  title={
                    anthropicReady
                      ? ''
                      : 'Configure an Anthropic API key in Org Settings → Anthropic Integration to dispatch the remote coding agent.'
                  }
                  placement="left"
                  arrow
                  disableHoverListener={anthropicReady}
                >
                  <span>
                    <MenuItem
                      onClick={handleRemoteImplementation}
                      disabled={!anthropicReady}
                      sx={{ alignItems: 'flex-start', py: 1.5, gap: 1.5, minWidth: 320 }}
                    >
                      <Box sx={{ mt: 0.25, flexShrink: 0, display: 'flex' }}>
                        {anthropicReady ? <Cloud size={20} /> : <AlertCircle size={20} color="var(--color-warning, #d97706)" />}
                      </Box>
                      <Box>
                        <Typography variant="body2" fontWeight={600}>Implement via Remote Agents</Typography>
                        <Typography variant="caption" color="text.secondary">
                          {anthropicReady
                            ? 'Platform spawns Coding agents on the host to work on every task.'
                            : (
                              <>
                                Anthropic API key required —{' '}
                                <RouterLink to={settingsUrl} style={{ color: 'inherit', textDecoration: 'underline' }}>
                                  configure in Org Settings
                                </RouterLink>
                                .
                              </>
                            )}
                        </Typography>
                      </Box>
                    </MenuItem>
                  </span>
                </Tooltip>
                <MenuItem onClick={handleLocalImplementation} sx={{ alignItems: 'flex-start', py: 1.5, gap: 1.5, minWidth: 320 }}>
                  <Box sx={{ mt: 0.25, flexShrink: 0, display: 'flex' }}>
                    <Laptop size={20} />
                  </Box>
                  <Box>
                    <Typography variant="body2" fontWeight={600}>Implement Locally</Typography>
                    <Typography variant="caption" color="text.secondary">
                      Use the Platofrm plugin in your own Coding Agent session.
                    </Typography>
                  </Box>
                </MenuItem>
              </Menu>
            </>
          )}

          {githubProjectUrl && (
            <Button
              variant="outlined"
              size="small"
              component="a"
              href={githubProjectUrl}
              target="_blank"
              rel="noopener noreferrer"
              startIcon={<Github size={16} />}
              endIcon={<ExternalLink size={14} />}
              sx={{
                textTransform: 'none',
                fontWeight: 500,
                '& .MuiButton-startIcon': { mr: 0.75 },
                '& .MuiButton-endIcon': { ml: 0.5 },
              }}
            >
              GitHub Project
            </Button>
          )}
        </Stack>
      </Stack>

      <Dialog open={showLocalGuide} onClose={() => setShowLocalGuide(false)} maxWidth="sm" fullWidth>
        <DialogTitle>
          <Stack direction="row" spacing={1} alignItems="center">
            <Laptop size={20} />
            <Box>Implement Locally with Claude Code</Box>
          </Stack>
        </DialogTitle>
        <DialogContent dividers>
          <Typography variant="body2" sx={{ mb: 2 }}>
            Each task above has a corresponding GitHub issue (open the{' '}
            <Box component="span" sx={{ display: 'inline-flex', verticalAlign: 'middle', mx: 0.5 }}>
              <ExternalLink size={12} />
            </Box>
            {' '}link on a row), a feature branch, and a draft PR already prepared. Work directly on
            GitHub from a regular Claude Code session — no platform plugin needed.
          </Typography>

          <Typography variant="subtitle2" sx={{ mt: 2, mb: 1 }}>1. Clone the repo and check out the task branch</Typography>
          <Box
            component="pre"
            sx={{ p: 1.5, bgcolor: 'action.hover', borderRadius: 1, fontSize: '0.8rem', overflowX: 'auto', fontFamily: 'monospace', m: 0 }}
          >
{`gh repo clone <repo>
cd <repo>
git checkout <task-branch>`}
          </Box>

          <Typography variant="subtitle2" sx={{ mt: 2, mb: 1 }}>2. Implement, push, and mark the PR ready</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            Run Claude Code in the repo. When the work is done:
          </Typography>
          <Box
            component="pre"
            sx={{ p: 1.5, bgcolor: 'action.hover', borderRadius: 1, fontSize: '0.8rem', overflowX: 'auto', fontFamily: 'monospace', m: 0 }}
          >
{`git push origin HEAD
gh pr ready <pr-number>`}
          </Box>

          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>
            GitHub webhooks drive task status here — readying the PR advances the task; merging it kicks
            off the build automatically.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setShowLocalGuide(false)}>Close</Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
