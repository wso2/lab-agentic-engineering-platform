import { useCallback, useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
  Typography,
  Divider,
} from '@wso2/oxygen-ui';
import { Key, AlertCircle, RefreshCw } from '@wso2/oxygen-ui-icons-react';
import {
  orgAnthropicApi,
  type OrgAnthropicProjection,
} from '../services/api/orgAnthropic';
import { ApiError } from '../services/api/rest';

/**
 * OrgAnthropicSettings — Org Settings → Anthropic Integration.
 *
 * Mirrors the GitHub Integration page shape. Reads projection via
 * GET /api/v1/organizations/{orgHandle}/anthropic. Renders one of:
 *   - "Not configured" panel with a single password input.
 *   - "Connected" badge with prefix + last4 + last validation + actions.
 *   - "Validation failed" badge with the structured error code.
 *
 * See docs/design/anthropic-key-dual-token.md §2.
 */
export default function OrgAnthropicSettings() {
  const { orgId } = useParams();
  const orgHandle = orgId ?? 'default';

  const [projection, setProjection] = useState<OrgAnthropicProjection | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showReplace, setShowReplace] = useState(false);
  const [showDisconnect, setShowDisconnect] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [apiKey, setApiKey] = useState('');
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [formCode, setFormCode] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const proj = await orgAnthropicApi.getStatus(orgHandle);
      setProjection(proj);
    } catch (err) {
      const e = err as Error;
      setError(e.message || 'Failed to load Anthropic integration status');
    } finally {
      setLoading(false);
    }
  }, [orgHandle]);

  useEffect(() => {
    load();
  }, [load]);

  const handleSave = async () => {
    setSaving(true);
    setFormError(null);
    setFormCode(null);
    try {
      const proj = await orgAnthropicApi.connect(orgHandle, apiKey.trim());
      setProjection(proj);
      setApiKey('');
      setShowReplace(false);
    } catch (err) {
      if (err instanceof ApiError) {
        setFormError(err.message);
        const code = (err as ApiError & { code?: string }).code;
        if (code) setFormCode(code);
      } else {
        setFormError((err as Error).message || 'Failed to save key');
      }
    } finally {
      setSaving(false);
    }
  };

  const handleDisconnect = async () => {
    setDisconnecting(true);
    setError(null);
    try {
      await orgAnthropicApi.disconnect(orgHandle);
      setProjection({ ocOrgId: orgHandle, status: 'not_connected' });
      setShowDisconnect(false);
    } catch (err) {
      setError((err as Error).message || 'Failed to disconnect');
    } finally {
      setDisconnecting(false);
    }
  };

  if (loading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', p: 4 }}>
        <CircularProgress size={32} />
      </Box>
    );
  }

  if (error) {
    return (
      <Alert severity="error" action={<Button onClick={load} startIcon={<RefreshCw size={16} />}>Retry</Button>}>
        {error}
      </Alert>
    );
  }

  const status = projection?.status ?? 'not_connected';
  const isActive = status === 'active';
  const isInvalid = status === 'invalid';
  const showForm = status === 'not_connected' || showReplace;

  return (
    <Box>
      <Stack direction="row" alignItems="center" gap={1.5} sx={{ mb: 1 }}>
        <Key size={22} />
        <Typography variant="h5" fontWeight={700}>Anthropic Integration</Typography>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3, maxWidth: 700 }}>
        Configure an Anthropic API key to dispatch the remote coding agent for this organization.
        Requirements, architecture and task generation will use the platform-provided key as a
        fallback if you don&apos;t configure one here.
      </Typography>

      {isActive && projection && !showReplace && (
        <Card>
          <CardContent>
            <Stack gap={2}>
              <Stack direction="row" alignItems="center" gap={1.5}>
                <Chip size="small" color="success" label="Connected" />
                <Typography variant="body1" sx={{ fontFamily: 'monospace' }}>
                  {projection.keyPrefix}…{projection.keyLast4}
                </Typography>
              </Stack>
              {projection.lastValidatedAt && (
                <Typography variant="caption" color="text.secondary">
                  Last validated {new Date(projection.lastValidatedAt).toLocaleString()}
                </Typography>
              )}
              <Divider />
              <Stack direction="row" gap={1.5}>
                <Button variant="outlined" onClick={() => { setShowReplace(true); setApiKey(''); setFormError(null); }}>
                  Replace key
                </Button>
                <Button variant="outlined" color="error" onClick={() => setShowDisconnect(true)}>
                  Disconnect
                </Button>
              </Stack>
            </Stack>
          </CardContent>
        </Card>
      )}

      {isInvalid && projection && !showReplace && (
        <Card>
          <CardContent>
            <Stack gap={2}>
              <Stack direction="row" alignItems="center" gap={1.5}>
                <Chip size="small" color="error" label="Validation failed" icon={<AlertCircle size={14} />} />
                <Typography variant="body1" sx={{ fontFamily: 'monospace' }}>
                  {projection.keyPrefix}…{projection.keyLast4}
                </Typography>
              </Stack>
              {projection.validationError && (
                <Alert severity="warning">{projection.validationError}</Alert>
              )}
              <Typography variant="body2" color="text.secondary">
                Requirements / architecture / task generation continue to work via the platform
                fallback. The coding agent cannot dispatch until a valid key is configured.
              </Typography>
              <Divider />
              <Stack direction="row" gap={1.5}>
                <Button variant="contained" onClick={() => { setShowReplace(true); setApiKey(''); setFormError(null); }}>
                  Replace key
                </Button>
                <Button variant="outlined" color="error" onClick={() => setShowDisconnect(true)}>
                  Disconnect
                </Button>
              </Stack>
            </Stack>
          </CardContent>
        </Card>
      )}

      {showForm && (
        <Card>
          <CardContent>
            <Stack gap={2}>
              <Typography variant="h6">
                {status === 'not_connected' ? 'Configure your Anthropic API key' : 'Replace your Anthropic API key'}
              </Typography>
              <Typography variant="body2" color="text.secondary">
                The key is encrypted at rest and never leaves WSO2 Cloud&apos;s control plane.
                Get a key at <a href="https://console.anthropic.com/settings/keys" target="_blank" rel="noreferrer">console.anthropic.com</a>.
              </Typography>
              <TextField
                type="password"
                label="API key"
                placeholder="sk-ant-…"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                autoComplete="off"
                fullWidth
                error={!!formError}
                helperText={formError ? `${formError}${formCode ? ` (${formCode})` : ''}` : 'Paste your full Anthropic API key. We validate against Anthropic before saving.'}
              />
              <Stack direction="row" gap={1.5}>
                <Button
                  variant="contained"
                  disabled={!apiKey.trim() || saving}
                  onClick={handleSave}
                >
                  {saving ? 'Validating…' : 'Save & validate'}
                </Button>
                {(status === 'active' || status === 'invalid') && (
                  <Button variant="outlined" onClick={() => { setShowReplace(false); setApiKey(''); setFormError(null); }}>
                    Cancel
                  </Button>
                )}
              </Stack>
            </Stack>
          </CardContent>
        </Card>
      )}

      <Dialog open={showDisconnect} onClose={() => setShowDisconnect(false)}>
        <DialogTitle>Disconnect Anthropic API key?</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            This removes the encrypted key and prevents new coding-agent dispatches for this org.
            Workflow runs currently in flight will continue using the credentials they already have.
            Requirements / architecture / task generation will fall back to the platform-provided key.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setShowDisconnect(false)} disabled={disconnecting}>Cancel</Button>
          <Button onClick={handleDisconnect} color="error" variant="contained" disabled={disconnecting}>
            {disconnecting ? 'Disconnecting…' : 'Disconnect'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
