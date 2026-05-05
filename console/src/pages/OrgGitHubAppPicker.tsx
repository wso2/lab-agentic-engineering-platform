import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { Github, ArrowLeft } from '@wso2/oxygen-ui-icons-react';
import { orgGithubApi, type AppInstallationSummary } from '../services/api/orgGithub';

/**
 * OrgGitHubAppPicker — rendered when the connect callback found 2+
 * installations the user administers. The candidates list arrives via
 * the `?candidates=<base64>` query param (server-encoded; never
 * regenerated client-side). User picks one → re-enters the connect flow
 * with installationId pinned in the new state JWT, which authorizes
 * the bind for that specific install.
 */
export default function OrgGitHubAppPicker() {
  const { orgId } = useParams();
  const orgHandle = orgId ?? 'default';
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();

  const [submittingId, setSubmittingId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  const candidates = useMemo<AppInstallationSummary[] | null>(() => {
    const raw = searchParams.get('candidates');
    if (!raw) return null;
    try {
      // base64url decode
      const b64 = raw.replace(/-/g, '+').replace(/_/g, '/');
      const json = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
      const parsed = JSON.parse(json);
      if (Array.isArray(parsed)) return parsed as AppInstallationSummary[];
      return null;
    } catch {
      return null;
    }
  }, [searchParams]);

  // Empty/invalid candidates → bounce back to settings with an error banner.
  useEffect(() => {
    if (candidates === null || candidates.length === 0) {
      navigate(`/organizations/${orgHandle}/settings/github?error=picker_invalid`, { replace: true });
    }
  }, [candidates, navigate, orgHandle]);

  const handlePick = async (installationId: number) => {
    setSubmittingId(installationId);
    setError(null);
    try {
      const { authorizeUrl } = await orgGithubApi.startConnect(orgHandle, installationId);
      window.location.assign(authorizeUrl);
    } catch (err) {
      setError((err as Error).message || 'Could not start bind');
      setSubmittingId(null);
    }
  };

  if (!candidates || candidates.length === 0) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', p: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  return (
    <Stack gap={2}>
      <Button
        variant="text"
        startIcon={<ArrowLeft size={16} />}
        onClick={() => navigate(`/organizations/${orgHandle}/settings/github`)}
        sx={{ alignSelf: 'flex-start' }}
      >
        Back to GitHub Integration
      </Button>

      <Stack direction="row" alignItems="center" gap={1.5}>
        <Github size={24} />
        <Typography variant="h5" fontWeight={700}>Pick a GitHub installation</Typography>
      </Stack>
      <Typography variant="body1" color="text.secondary">
        You administer multiple GitHub installations of the ASDLC App. Pick the one you want to connect to this organization.
      </Typography>

      {error && <Alert severity="error" onClose={() => setError(null)}>{error}</Alert>}

      <Stack gap={1.5}>
        {candidates.map((c) => (
          <Card key={c.installationId} sx={{ borderRadius: 2 }}>
            <CardContent>
              <Stack direction="row" justifyContent="space-between" alignItems="center" gap={2}>
                <Stack direction="row" alignItems="center" gap={1.5}>
                  <Github size={20} />
                  <Stack>
                    <Typography variant="body1" fontWeight={600}>{c.accountLogin}</Typography>
                    <Stack direction="row" gap={1} alignItems="center" sx={{ mt: 0.5 }}>
                      <Chip label={c.accountType} size="small" />
                      <Typography variant="caption" color="text.secondary">
                        Installation #{c.installationId}
                      </Typography>
                    </Stack>
                  </Stack>
                </Stack>
                <Button
                  variant="contained"
                  onClick={() => handlePick(c.installationId)}
                  disabled={submittingId !== null}
                  startIcon={submittingId === c.installationId ? <CircularProgress size={16} /> : null}
                >
                  {submittingId === c.installationId ? 'Starting…' : 'Bind'}
                </Button>
              </Stack>
            </CardContent>
          </Card>
        ))}
      </Stack>
    </Stack>
  );
}
