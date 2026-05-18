import { useState, useEffect, useCallback } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  Stack,
  Typography,
  Divider,
} from '@wso2/oxygen-ui';
import { Github, RefreshCw, AlertCircle } from '@wso2/oxygen-ui-icons-react';
import ConnectAppButton from '../components/ConnectAppButton';
import ConnectPATForm from '../components/ConnectPATForm';
import IdentityDriftBanner from '../components/IdentityDriftBanner';
import ReachReconciliationBanner from '../components/ReachReconciliationBanner';
import { orgGithubApi, type OrgGithubProjection } from '../services/api/orgGithub';

/**
 * OrgGitHubSettings — Phase 2 PR B integration page.
 *
 * Reads projection via GET /api/v1/organizations/{orgHandle}/github.
 * Renders one of:
 *   §10.3 (not connected — App + PAT cards side by side)
 *   §10.4 (connected via App)
 *   §10.5 (connected via PAT, optional IdentityDriftBanner)
 *
 * Per phase2.md §10.
 */
export default function OrgGitHubSettings() {
  const { orgId } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const orgHandle = orgId ?? 'default';

  const [projection, setProjection] = useState<OrgGithubProjection | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showDisconnect, setShowDisconnect] = useState(false);
  const [uninstallOnDisconnect, setUninstallOnDisconnect] = useState(true);
  const [showReplace, setShowReplace] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  const callbackError = searchParams.get('error');
  const callbackConnected = searchParams.get('connected');

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const proj = await orgGithubApi.getStatus(orgHandle);
      setProjection(proj);
    } catch (err) {
      const e = err as Error;
      setError(e.message || 'Failed to load GitHub integration status');
    } finally {
      setLoading(false);
    }
  }, [orgHandle]);

  useEffect(() => {
    load();
  }, [load]);

  // Clear callback query params after first read.
  useEffect(() => {
    if (callbackConnected || callbackError) {
      const t = setTimeout(() => {
        searchParams.delete('connected');
        searchParams.delete('error');
        setSearchParams(searchParams, { replace: true });
      }, 200);
      return () => clearTimeout(t);
    }
    return undefined;
  }, [callbackConnected, callbackError, searchParams, setSearchParams]);

  const handleDisconnect = async () => {
    setDisconnecting(true);
    setError(null);
    try {
      const isApp = projection?.kind === 'app-installation';
      await orgGithubApi.disconnect(orgHandle, isApp ? uninstallOnDisconnect : false);
      setShowDisconnect(false);
      await load();
    } catch (err) {
      setError((err as Error).message || 'Disconnect failed');
    } finally {
      setDisconnecting(false);
    }
  };

  if (loading && !projection) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', p: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  const status = projection?.status ?? 'not_connected';
  const isConnected = status === 'active' || status === 'suspended' || status === 'disconnecting';

  const callbackErrorMessages: Record<string, string> = {
    cross_mode: 'Cross-mode change refused. Disconnect the existing connection first, then reconnect via the other mode.',
    connect_failed: 'GitHub App connect failed. Check git-service logs and try again.',
    oauth_unauthorized: 'You are not an admin of the GitHub installation you tried to bind. Sign in to GitHub as an organization owner / app installer and try again.',
    app_bind_not_configured: 'GitHub App OAuth is not configured on this deployment. Set GITHUB_CLIENT_SECRET in deployments/.env and re-run setup-prerequisites.sh.',
    callback_invalid: 'GitHub callback was missing required parameters. Click Connect again to retry.',
    picker_invalid: 'Could not parse the install picker payload. Click Connect again to retry.',
    user_account_install_unsupported: 'The GitHub App was installed on a personal user account, which cannot host repositories created by integrations. Please uninstall it from your user account on github.com, then click Connect again and install on a GitHub Organization you administer.',
    installation_not_found: 'The GitHub installation could not be found. It may have been uninstalled — click Connect again to start over.',
  };

  return (
    <Stack gap={2}>
      <Stack direction="row" alignItems="center" gap={1.5}>
        <Github size={24} />
        <Typography variant="h5" fontWeight={700}>GitHub Integration</Typography>
      </Stack>

      {callbackError && (
        <Alert severity="error" onClose={() => { searchParams.delete('error'); setSearchParams(searchParams, { replace: true }); }}>
          {callbackErrorMessages[callbackError] ?? callbackError}
        </Alert>
      )}
      {callbackConnected && (
        <Alert severity="success" onClose={() => { searchParams.delete('connected'); setSearchParams(searchParams, { replace: true }); }}>
          GitHub App connected successfully.
        </Alert>
      )}
      {error && <Alert severity="error" onClose={() => setError(null)}>{error}</Alert>}

      {!isConnected && (
        <NotConnectedPanel orgHandle={orgHandle} onConnected={load} />
      )}

      {isConnected && projection?.kind === 'app-installation' && (
        <ConnectedAppPanel
          projection={projection}
          onDisconnect={() => setShowDisconnect(true)}
        />
      )}

      {isConnected && projection?.kind === 'user-pat' && (
        <ConnectedPATPanel
          projection={projection}
          showReplace={showReplace}
          orgHandle={orgHandle}
          onReplaceToggle={() => setShowReplace((v) => !v)}
          onDisconnect={() => setShowDisconnect(true)}
          onReplaceSuccess={(p) => {
            setProjection(p);
            setShowReplace(false);
          }}
        />
      )}

      {/* Disconnect confirmation dialog */}
      <Dialog open={showDisconnect} onClose={() => !disconnecting && setShowDisconnect(false)}>
        <DialogTitle>Disconnect GitHub?</DialogTitle>
        <DialogContent>
          <Stack gap={1.5}>
            <Typography variant="body2">
              Disconnecting will abandon in-flight tasks for this organization. Builds already running will continue.
              You can reconnect afterwards in either mode.
            </Typography>
            {projection?.kind === 'app-installation' && (
              <FormControlLabel
                control={
                  <Checkbox
                    checked={uninstallOnDisconnect}
                    onChange={(e) => setUninstallOnDisconnect(e.target.checked)}
                    disabled={disconnecting}
                  />
                }
                label={
                  <Stack>
                    <Typography variant="body2">Also uninstall App from GitHub (recommended)</Typography>
                    <Typography variant="caption" color="text.secondary">
                      Removes the install on github.com so it doesn't linger as an orphan. Uncheck to keep the install for re-adoption.
                    </Typography>
                  </Stack>
                }
              />
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setShowDisconnect(false)} disabled={disconnecting}>Cancel</Button>
          <Button
            onClick={handleDisconnect}
            color="error"
            variant="contained"
            disabled={disconnecting}
            startIcon={disconnecting ? <CircularProgress size={16} /> : null}
          >
            {disconnecting ? 'Disconnecting…' : 'Disconnect'}
          </Button>
        </DialogActions>
      </Dialog>
    </Stack>
  );
}

// ----------------------------------------------------------------------------
// Sub-panels
// ----------------------------------------------------------------------------

function NotConnectedPanel({ orgHandle, onConnected }: { orgHandle: string; onConnected: () => void }) {
  return (
    <Stack gap={2}>
      <Typography variant="body1" color="text.secondary">
        Connect this organization to GitHub so the Agentic Engineering Platform can provision repos, open issues, and run agents on your behalf.
      </Typography>

      {/* App card — primary, recommended */}
      <Card sx={{ border: 2, borderColor: 'primary.main', borderRadius: 2 }}>
        <CardContent>
          <Stack direction="row" alignItems="center" gap={1.5} sx={{ mb: 1.5 }}>
            <Github size={22} />
            <Typography variant="h6" fontWeight={700}>GitHub App</Typography>
            <Chip label="recommended" size="small" color="primary" />
          </Stack>
          <Stack gap={0.5} sx={{ mb: 2 }}>
            <Typography variant="body2">• Per-repo access — you choose which repos</Typography>
            <Typography variant="body2">• Bot identity (asdlc-platform[bot]) on commits</Typography>
            <Typography variant="body2">• Tokens auto-rotate hourly</Typography>
            <Typography variant="body2">• App-wide webhook delivery</Typography>
          </Stack>
          <Alert severity="info" icon={<AlertCircle size={18} />} sx={{ mb: 2 }}>
            <Typography variant="body2" fontWeight={600}>Install on a GitHub Organization, not a personal account.</Typography>
            <Typography variant="caption" color="text.secondary">
              The Agentic Engineering Platform creates repositories on the connected GitHub account. GitHub does not let integrations create repos on personal user accounts — pick (or create) a GitHub Org you administer when prompted.
            </Typography>
          </Alert>
          <ConnectAppButton orgHandle={orgHandle} />
        </CardContent>
      </Card>

      {/* PAT card — secondary */}
      <ConnectPATForm
        orgHandle={orgHandle}
        mode="create"
        onSuccess={() => onConnected()}
      />
    </Stack>
  );
}

function ConnectedAppPanel({ projection, onDisconnect }: { projection: OrgGithubProjection; onDisconnect: () => void }) {
  return (
    <Stack gap={2}>
      {projection.status === 'suspended' && (
        <Alert severity="warning" icon={<AlertCircle size={20} />}>
          GitHub App is suspended on the GitHub side. New work is blocked until the install is unsuspended on GitHub.
        </Alert>
      )}

      {/* Phase 2 PR D §10.9 — surfaces tasks abandoned by an
         installation_repositories.removed cascade in the last 24h. */}
      <ReachReconciliationBanner ocOrgId={projection.ocOrgId} />


      <Card sx={{ borderRadius: 2 }}>
        <CardContent>
          <Stack direction="row" justifyContent="space-between" alignItems="flex-start" sx={{ mb: 2 }}>
            <Stack direction="row" alignItems="center" gap={1.5}>
              <Github size={22} />
              <Stack>
                <Typography variant="h6" fontWeight={700}>Connected via GitHub App</Typography>
                <Stack direction="row" gap={1} alignItems="center" sx={{ mt: 0.5 }}>
                  <Chip
                    label={projection.status}
                    size="small"
                    color={projection.status === 'active' ? 'success' : projection.status === 'suspended' ? 'warning' : 'default'}
                  />
                </Stack>
              </Stack>
            </Stack>
            <Button variant="outlined" color="error" onClick={onDisconnect}>Disconnect</Button>
          </Stack>

          <Divider sx={{ mb: 2 }} />

          <Stack gap={1.25} sx={{ mb: 2 }}>
            <DetailRow label="Account" value={projection.githubLogin ?? '—'} />
            <DetailRow label="Identity" value={projection.identityLogin ?? '—'} secondary="(commits, comments, PRs use this identity)" />
            {projection.installationId !== undefined && (
              <DetailRow label="Installation" value={`#${projection.installationId}`} />
            )}
            <DetailRow label="Connected" value={fmt(projection.connectedAt)} />
            <DetailRow label="Last validated" value={fmt(projection.lastValidatedAt)} />
          </Stack>
        </CardContent>
      </Card>

    </Stack>
  );
}

function ConnectedPATPanel({
  projection,
  showReplace,
  orgHandle,
  onReplaceToggle,
  onDisconnect,
  onReplaceSuccess,
}: {
  projection: OrgGithubProjection;
  showReplace: boolean;
  orgHandle: string;
  onReplaceToggle: () => void;
  onDisconnect: () => void;
  onReplaceSuccess: (p: OrgGithubProjection) => void;
}) {
  return (
    <Stack gap={2}>
      {projection.identityChangedAt && (
        <IdentityDriftBanner
          ocOrgId={projection.ocOrgId}
          identityChangedAt={projection.identityChangedAt}
          prevIdentityLogin={projection.prevIdentityLogin}
          identityLogin={projection.identityLogin ?? ''}
        />
      )}

      <Card sx={{ borderRadius: 2 }}>
        <CardContent>
          <Stack direction="row" justifyContent="space-between" alignItems="flex-start" sx={{ mb: 2 }}>
            <Stack direction="row" alignItems="center" gap={1.5}>
              <Github size={22} />
              <Stack>
                <Typography variant="h6" fontWeight={700}>Connected via Personal Access Token</Typography>
                <Stack direction="row" gap={1} alignItems="center" sx={{ mt: 0.5 }}>
                  <Chip label={projection.status} size="small" color={projection.status === 'active' ? 'success' : 'default'} />
                </Stack>
              </Stack>
            </Stack>
            <Stack direction="row" gap={1}>
              <Button variant="outlined" startIcon={<RefreshCw size={16} />} onClick={onReplaceToggle}>
                {showReplace ? 'Cancel replace' : 'Replace PAT'}
              </Button>
              <Button variant="outlined" color="error" onClick={onDisconnect}>Disconnect</Button>
            </Stack>
          </Stack>

          <Divider sx={{ mb: 2 }} />

          <Stack gap={1.25}>
            <DetailRow label="GitHub login" value={projection.githubLogin ?? '—'} />
            <DetailRow
              label="Identity"
              value={projection.identityLogin ?? '—'}
              secondary={`${projection.identityName ?? ''} <${projection.identityEmail ?? ''}>`}
            />
            <DetailRow label="Connected" value={fmt(projection.connectedAt)} />
            <DetailRow label="Last validated" value={fmt(projection.lastValidatedAt)} />
          </Stack>
        </CardContent>
      </Card>

      {showReplace && (
        <ConnectPATForm
          orgHandle={orgHandle}
          mode="replace"
          defaultGithubLogin={projection.githubLogin}
          onSuccess={onReplaceSuccess}
        />
      )}
    </Stack>
  );
}

function DetailRow({ label, value, secondary }: { label: string; value: string; secondary?: string }) {
  return (
    <Stack direction="row" gap={2}>
      <Typography variant="body2" color="text.secondary" sx={{ minWidth: 140 }}>{label}</Typography>
      <Stack>
        <Typography variant="body2">{value}</Typography>
        {secondary && <Typography variant="caption" color="text.secondary">{secondary}</Typography>}
      </Stack>
    </Stack>
  );
}

function fmt(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString();
}
