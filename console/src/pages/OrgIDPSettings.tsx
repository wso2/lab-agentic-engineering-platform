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
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from '@wso2/oxygen-ui';
import { ShieldCheck, RefreshCw, AlertCircle, Copy, Edit2, Search } from '@wso2/oxygen-ui-icons-react';
import { orgIDPApi, type OrgIDPProfile } from '../services/api/orgIDP';

// OrgIDPSettings — Org Settings → IDP Integration.
//
// Phase 3 read state + Phase 7 editable picker. The page renders:
//   - Provisioning state (kind, issuer, jwks_url, publisher_client_id)
//   - "Pre-provisioning" hint when no profile exists yet
//   - "Edit" button → kind picker (platform | asgardeo | custom) +
//     issuer + JWKS URL fields. "Auto-discover" button calls the
//     BFF /api/v1/idp/discover endpoint to populate JWKS URL from
//     the standard OIDC discovery doc.
//   - Admin "Rotate secret" button (Phase 3 emergency rotation).
//
// Per docs/design/api-platform-integration.md §6 Phases 3 + 4 + 7.

type IDPKind = 'platform' | 'asgardeo' | 'custom';

export default function OrgIDPSettings() {
  const { orgId } = useParams();
  const orgHandle = orgId ?? 'default';

  const [profile, setProfile] = useState<OrgIDPProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [rotating, setRotating] = useState(false);
  const [rotateError, setRotateError] = useState<string | null>(null);
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  const [showRotateConfirm, setShowRotateConfirm] = useState(false);

  // Edit-mode state (Phase 7)
  const [showEdit, setShowEdit] = useState(false);
  const [editKind, setEditKind] = useState<IDPKind>('platform');
  const [editIssuer, setEditIssuer] = useState('');
  const [editJWKS, setEditJWKS] = useState('');
  const [discovering, setDiscovering] = useState(false);
  const [discoverError, setDiscoverError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const p = await orgIDPApi.getProfile(orgHandle);
      setProfile(p);
    } catch (err) {
      setError((err as Error).message || 'Failed to load IDP profile');
    } finally {
      setLoading(false);
    }
  }, [orgHandle]);

  useEffect(() => {
    void load();
  }, [load]);

  const openEdit = useCallback(() => {
    setEditKind((profile?.kind ?? 'platform') as IDPKind);
    setEditIssuer(profile?.issuer ?? '');
    setEditJWKS(profile?.jwksUrl ?? '');
    setDiscoverError(null);
    setSaveError(null);
    setShowEdit(true);
  }, [profile]);

  const handleDiscover = useCallback(async () => {
    setDiscoverError(null);
    if (!editIssuer) {
      setDiscoverError('Enter an issuer URL first.');
      return;
    }
    setDiscovering(true);
    try {
      const md = await orgIDPApi.discoverIssuer(editIssuer);
      setEditIssuer(md.issuer);
      setEditJWKS(md.jwksUrl);
    } catch (err) {
      setDiscoverError((err as Error).message || 'Discovery failed');
    } finally {
      setDiscovering(false);
    }
  }, [editIssuer]);

  const handleSave = useCallback(async () => {
    setSaveError(null);
    setSaving(true);
    try {
      const updated = await orgIDPApi.updateProfile(orgHandle, {
        kind: editKind,
        issuer: editIssuer,
        jwksUrl: editJWKS,
      });
      setProfile(updated);
      setShowEdit(false);
    } catch (err) {
      setSaveError((err as Error).message || 'Failed to update profile');
    } finally {
      setSaving(false);
    }
  }, [orgHandle, editKind, editIssuer, editJWKS]);

  const handleRotate = useCallback(async () => {
    setShowRotateConfirm(false);
    setRotateError(null);
    setRotating(true);
    try {
      const { clientSecret } = await orgIDPApi.rotateSecret(orgHandle);
      setRevealedSecret(clientSecret);
      await load();
    } catch (err) {
      setRotateError((err as Error).message || 'Failed to rotate client secret');
    } finally {
      setRotating(false);
    }
  }, [orgHandle, load]);

  if (loading) {
    return (
      <Card>
        <CardContent>
          <Stack alignItems="center" sx={{ py: 4 }}>
            <CircularProgress size={20} />
          </Stack>
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return <Alert severity="error" icon={<AlertCircle size={20} />}>{error}</Alert>;
  }

  const provisioned = !!profile?.kind;
  const hasPublisher = !!profile?.publisherClientId;
  const kindLabel = profile?.kind === 'platform' ? 'Platform IDP (Thunder)' : (profile?.kind ?? '');

  return (
    <Stack gap={3}>
      <Box>
        <Stack direction="row" alignItems="center" gap={1.5} sx={{ mb: 0.5 }}>
          <ShieldCheck size={20} />
          <Typography variant="h6" fontWeight={600}>IDP Integration</Typography>
        </Stack>
        <Typography variant="body2" color="text.secondary">
          The organisation's identity provider for protected component APIs.
          Provisioned automatically when a component's design is marked
          <code style={{ marginInline: 4 }}>api.security: required</code>.
          Switch to Asgardeo or a custom OIDC provider via Edit.
        </Typography>
      </Box>

      {!provisioned && (
        <Alert severity="info" variant="outlined">
          No IDP profile configured yet. The platform IDP (Thunder) will be
          assigned automatically on first protected-component deploy, or you
          can configure a BYO-IDP now via Edit.
        </Alert>
      )}

      {provisioned && (
        <Card>
          <CardContent>
            <Stack gap={2}>
              <Stack direction="row" alignItems="center" gap={1} justifyContent="space-between">
                <Stack direction="row" alignItems="center" gap={1}>
                  <Typography variant="body2" color="text.secondary" sx={{ minWidth: 140 }}>
                    Kind
                  </Typography>
                  <Chip
                    size="small"
                    label={kindLabel}
                    color="success"
                    variant="filled"
                    data-testid="idp-kind-chip"
                  />
                </Stack>
                <Button
                  size="small"
                  variant="outlined"
                  startIcon={<Edit2 size={14} />}
                  onClick={openEdit}
                  data-testid="idp-edit-btn"
                >
                  Edit
                </Button>
              </Stack>

              <Stack direction="row" alignItems="flex-start" gap={1}>
                <Typography variant="body2" color="text.secondary" sx={{ minWidth: 140 }}>
                  Issuer
                </Typography>
                <Typography variant="body2" sx={{ wordBreak: 'break-all' }} data-testid="idp-issuer">
                  {profile!.issuer ?? '—'}
                </Typography>
              </Stack>

              <Stack direction="row" alignItems="flex-start" gap={1}>
                <Typography variant="body2" color="text.secondary" sx={{ minWidth: 140 }}>
                  JWKS URL
                </Typography>
                <Typography variant="body2" sx={{ wordBreak: 'break-all' }} data-testid="idp-jwks-url">
                  {profile!.jwksUrl ?? '—'}
                </Typography>
              </Stack>

              <Stack direction="row" alignItems="center" gap={1}>
                <Typography variant="body2" color="text.secondary" sx={{ minWidth: 140 }}>
                  Publisher client
                </Typography>
                {hasPublisher ? (
                  <Stack direction="row" alignItems="center" gap={1}>
                    <Chip
                      size="small"
                      label={profile!.publisherClientId}
                      variant="outlined"
                      data-testid="idp-publisher-client-id"
                    />
                    <Chip size="small" label="Provisioned" color="success" variant="outlined" />
                  </Stack>
                ) : (
                  <Chip
                    size="small"
                    label="Pending first protected deploy"
                    color="warning"
                    variant="outlined"
                    data-testid="idp-publisher-pending"
                  />
                )}
              </Stack>

              {profile!.kind !== 'platform' && (
                <Alert severity="warning" icon={<AlertCircle size={16} />} sx={{ mt: 1 }}>
                  <Typography variant="body2">
                    A platform admin must add a matching <code>keymanager</code> entry to
                    <code style={{ marginInline: 4 }}>deployments/manifests/api-platform/gateway-config.yaml</code>
                    (under <code>jwtauth_v1.keymanagers</code>) and re-run
                    <code style={{ marginInline: 4 }}>setup-prerequisites.sh</code>
                    before the AP gateway will accept tokens from this IDP.
                    Automated keymanager registration is planned for v2.
                  </Typography>
                </Alert>
              )}

              {hasPublisher && (
                <Box sx={{ pt: 1 }}>
                  <Button
                    variant="outlined"
                    color="warning"
                    size="small"
                    startIcon={<RefreshCw size={14} />}
                    disabled={rotating}
                    onClick={() => setShowRotateConfirm(true)}
                    data-testid="idp-rotate-secret-btn"
                  >
                    {rotating ? 'Rotating…' : 'Rotate client secret'}
                  </Button>
                  {rotateError && (
                    <Alert severity="error" sx={{ mt: 1 }} icon={<AlertCircle size={16} />}>
                      {rotateError}
                    </Alert>
                  )}
                </Box>
              )}
            </Stack>
          </CardContent>
        </Card>
      )}

      {!provisioned && (
        <Box>
          <Button variant="contained" onClick={openEdit} startIcon={<Edit2 size={14} />} data-testid="idp-configure-btn">
            Configure IDP
          </Button>
        </Box>
      )}

      {/* Edit dialog */}
      <Dialog open={showEdit} onClose={() => setShowEdit(false)} maxWidth="md" fullWidth>
        <DialogTitle>IDP profile</DialogTitle>
        <DialogContent>
          <Stack gap={2.5} sx={{ pt: 1 }}>
            <FormControl fullWidth size="small">
              <InputLabel id="idp-kind-label">Kind</InputLabel>
              <Select
                labelId="idp-kind-label"
                label="Kind"
                value={editKind}
                onChange={(e) => setEditKind(e.target.value as IDPKind)}
                data-testid="idp-edit-kind-select"
              >
                <MenuItem value="platform">Platform IDP (Thunder)</MenuItem>
                <MenuItem value="asgardeo">Asgardeo</MenuItem>
                <MenuItem value="custom">Custom OIDC</MenuItem>
              </Select>
            </FormControl>

            <Stack direction="row" gap={1} alignItems="flex-start">
              <TextField
                fullWidth
                size="small"
                label="Issuer URL"
                placeholder="https://api.asgardeo.io/t/<org>"
                value={editIssuer}
                onChange={(e) => setEditIssuer(e.target.value)}
                disabled={editKind === 'platform'}
                helperText={editKind === 'platform' ? 'Locked when kind=platform' : 'OIDC issuer URL'}
                data-testid="idp-edit-issuer"
              />
              <Button
                variant="outlined"
                size="small"
                startIcon={discovering ? <CircularProgress size={14} /> : <Search size={14} />}
                disabled={discovering || editKind === 'platform' || !editIssuer}
                onClick={handleDiscover}
                sx={{ mt: 0.5, whiteSpace: 'nowrap' }}
                data-testid="idp-edit-discover-btn"
              >
                Auto-discover
              </Button>
            </Stack>
            {discoverError && (
              <Alert severity="error" icon={<AlertCircle size={16} />}>
                {discoverError}
              </Alert>
            )}

            <TextField
              fullWidth
              size="small"
              label="JWKS URL"
              placeholder="https://.../oauth2/jwks"
              value={editJWKS}
              onChange={(e) => setEditJWKS(e.target.value)}
              disabled={editKind === 'platform'}
              helperText={editKind === 'platform' ? 'Locked when kind=platform' : 'Public key set the AP gateway fetches'}
              data-testid="idp-edit-jwks"
            />

            {editKind !== 'platform' && (profile?.kind ?? 'platform') === 'platform' && (
              <Alert severity="warning" icon={<AlertCircle size={16} />}>
                Switching away from the platform IDP revokes the existing publisher
                app. A fresh one will be created in the new IDP on the next
                protected-component reconcile.
              </Alert>
            )}

            {saveError && (
              <Alert severity="error" icon={<AlertCircle size={16} />}>
                {saveError}
              </Alert>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setShowEdit(false)} disabled={saving}>Cancel</Button>
          <Button
            variant="contained"
            onClick={handleSave}
            disabled={saving}
            data-testid="idp-edit-save-btn"
          >
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Confirm rotate modal */}
      <Dialog open={showRotateConfirm} onClose={() => setShowRotateConfirm(false)}>
        <DialogTitle>Rotate publisher client secret?</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            The new secret will be shown ONCE. Any pod that mounted the old
            secret will need to be re-deployed with the new value before its
            existing token expires.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setShowRotateConfirm(false)}>Cancel</Button>
          <Button onClick={handleRotate} color="warning" variant="contained">
            Rotate
          </Button>
        </DialogActions>
      </Dialog>

      {/* Reveal-secret modal */}
      <Dialog open={!!revealedSecret} onClose={() => setRevealedSecret(null)} maxWidth="md" fullWidth>
        <DialogTitle>New publisher client secret</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }} icon={<AlertCircle size={20} />}>
            Copy this now — it won't be shown again. Subsequent reads only
            confirm the secret is present.
          </Alert>
          <Box
            sx={{
              p: 1.5,
              fontFamily: 'monospace',
              fontSize: 13,
              borderRadius: 1,
              bgcolor: 'background.default',
              border: 1,
              borderColor: 'divider',
              wordBreak: 'break-all',
            }}
            data-testid="idp-revealed-secret"
          >
            {revealedSecret}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button
            startIcon={<Copy size={14} />}
            onClick={() => {
              if (revealedSecret) {
                void navigator.clipboard.writeText(revealedSecret);
              }
            }}
          >
            Copy
          </Button>
          <Button onClick={() => setRevealedSecret(null)} variant="contained">
            Done
          </Button>
        </DialogActions>
      </Dialog>
    </Stack>
  );
}
