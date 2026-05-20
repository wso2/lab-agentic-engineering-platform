import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Link,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { type AbandonedTask, orgGithubApi } from '../services/api/orgGithub';

interface Props {
  ocOrgId: string;
}

/**
 * ReachReconciliationBanner — Phase 2 PR D §10.9 (App mode only).
 *
 * Surfaces tasks abandoned by an `installation_repositories.removed`
 * cascade (cause='repo.unselected') in the last 24 hours. The banner
 * carries a "View tasks" action that opens a dialog listing the
 * abandoned tasks with links back to their GitHub issues.
 *
 * Dismissal is per-(ocOrgId, mostRecentAbandonedAt). A new cascade
 * (newer abandoned timestamp) re-surfaces the banner — the dismissal
 * key changes so the prior `localStorage` flag no longer matches.
 *
 * The page-level caller decides when to mount this — the App-mode
 * connected panel mounts it; PAT mode does not (no installation_repositories
 * webhook in PAT mode, so this cause never fires there).
 */
export default function ReachReconciliationBanner({ ocOrgId }: Props) {
  const [tasks, setTasks] = useState<AbandonedTask[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dismissed, setDismissed] = useState(false);

  const fetchTasks = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const result = await orgGithubApi.listOrgTasks(ocOrgId, {
        status: 'abandoned',
        cause: 'repo.unselected',
        since: '24h',
      });
      setTasks(result);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [ocOrgId]);

  useEffect(() => {
    fetchTasks();
  }, [fetchTasks]);

  // Compute the dismissal key from the most recent abandonment timestamp.
  // Re-render after a new cascade naturally invalidates the dismissal.
  const mostRecent = (tasks ?? [])
    .map((t) => t.lastEventAt ?? '')
    .sort()
    .reverse()[0] ?? '';
  const dismissalKey = `asdlc.reach-reconciliation.dismissed:${ocOrgId}:${mostRecent}`;

  useEffect(() => {
    if (!mostRecent) {
      setDismissed(false);
      return;
    }
    setDismissed(localStorage.getItem(dismissalKey) === '1');
  }, [dismissalKey, mostRecent]);

  const handleDismiss = () => {
    if (!mostRecent) return;
    localStorage.setItem(dismissalKey, '1');
    setDismissed(true);
  };

  if (loading || error) {
    // Don't surface load errors as the banner itself — the page already
    // surfaces banner failures via the parent's error path. Silent here
    // is the right default (banner missing > banner showing wrong info).
    return null;
  }
  if (!tasks || tasks.length === 0) {
    return null;
  }
  if (dismissed) {
    return null;
  }

  const count = tasks.length;
  return (
    <>
      <Alert
        severity="warning"
        sx={{ mb: 2 }}
        action={
          <Stack direction="row" gap={1} alignItems="center">
            <Button color="inherit" size="small" onClick={() => setDialogOpen(true)}>
              View tasks
            </Button>
            <Button color="inherit" size="small" onClick={handleDismiss}>
              Dismiss
            </Button>
          </Stack>
        }
      >
        <Stack gap={0.5}>
          <Typography variant="subtitle2" fontWeight={600}>
            Repository selection changed on GitHub.
          </Typography>
          <Typography variant="body2">
            {count === 1
              ? '1 in-flight task was abandoned.'
              : `${count} in-flight tasks were abandoned.`}
          </Typography>
        </Stack>
      </Alert>

      <Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} maxWidth="md" fullWidth>
        <DialogTitle>Abandoned tasks (repository unselected)</DialogTitle>
        <DialogContent>
          <Stack gap={1.5} sx={{ minWidth: 320 }}>
            {tasks.map((t) => (
              <Box key={t.id} sx={{ borderBottom: '1px solid', borderColor: 'divider', pb: 1 }}>
                <Typography variant="subtitle2" fontWeight={600}>
                  {t.componentName}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Project: {t.projectId}
                  {t.lastEventAt ? ` · Abandoned ${new Date(t.lastEventAt).toLocaleString()}` : ''}
                </Typography>
                <Stack direction="row" gap={2} sx={{ mt: 0.5 }}>
                  {t.issueUrl ? (
                    <Link href={t.issueUrl} target="_blank" rel="noopener">
                      Issue #{t.issueNumber ?? '?'}
                    </Link>
                  ) : null}
                  {t.pullRequestUrl ? (
                    <Link href={t.pullRequestUrl} target="_blank" rel="noopener">
                      PR #{t.pullRequestNumber ?? '?'}
                    </Link>
                  ) : null}
                </Stack>
              </Box>
            ))}
            {tasks.length === 0 && (
              <Stack direction="row" gap={1} alignItems="center">
                <CircularProgress size={16} />
                <Typography>Loading…</Typography>
              </Stack>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDialogOpen(false)}>Close</Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
