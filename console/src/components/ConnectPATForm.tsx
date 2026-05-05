import { useState } from 'react';
import {
  Alert,
  Button,
  Card,
  CardContent,
  CircularProgress,
  IconButton,
  InputAdornment,
  Stack,
  TextField,
  Typography,
} from '@wso2/oxygen-ui';
import { Eye, EyeOff } from '@wso2/oxygen-ui-icons-react';
import { orgGithubApi, type OrgGithubProjection } from '../services/api/orgGithub';
import { ApiError } from '../services/api/rest';

interface Props {
  orgHandle: string;
  mode: 'create' | 'replace';
  defaultGithubLogin?: string;
  onSuccess: (proj: OrgGithubProjection) => void;
}

/**
 * ConnectPATForm — PAT-mode connect form, used both for first-time
 * connect (mode='create') and Replace PAT flows (mode='replace').
 *
 * Field-level errors come from the git-service validation chain via the
 * `code` field on ApiError. Mapping per phase2.md §10.7:
 *
 *   pat_invalid                 → password input
 *   pat_forbidden               → password input
 *   pat_no_repo_read            → password input
 *   pat_not_member              → github login input
 *   pat_membership_inactive     → github login input
 *   github_unreachable / *_error → top-level alert
 *
 * Per phase2.md §10.7.
 */
export default function ConnectPATForm({ orgHandle, mode, defaultGithubLogin, onSuccess }: Props) {
  const [githubLogin, setGithubLogin] = useState(defaultGithubLogin ?? '');
  const [pat, setPat] = useState('');
  const [showPat, setShowPat] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [topError, setTopError] = useState<string | null>(null);
  const [loginError, setLoginError] = useState<string | null>(null);
  const [patError, setPatError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setTopError(null);
    setLoginError(null);
    setPatError(null);

    if (!githubLogin.trim()) {
      setLoginError('GitHub login / org is required');
      return;
    }
    if (!pat) {
      setPatError('Personal access token is required');
      return;
    }

    setSubmitting(true);
    try {
      const proj = await orgGithubApi.connectPAT(orgHandle, pat, githubLogin.trim());
      onSuccess(proj);
    } catch (err) {
      const e = err as ApiError & { code?: string };
      switch (e.code) {
        case 'pat_invalid':
        case 'pat_forbidden':
        case 'pat_no_repo_read':
          setPatError(e.message);
          break;
        case 'pat_not_member':
        case 'pat_membership_inactive':
          setLoginError(e.message);
          break;
        case 'github_login_missing':
          setLoginError(e.message);
          break;
        case 'pat_missing':
          setPatError(e.message);
          break;
        default:
          setTopError(e.message);
      }
    } finally {
      setSubmitting(false);
    }
  };

  const formLabel = mode === 'replace' ? 'New personal access token' : 'Personal access token';
  const buttonLabel = mode === 'replace' ? 'Replace PAT' : 'Connect';

  return (
    <Card variant="outlined" sx={{ borderRadius: 2 }}>
      <CardContent>
        <Stack gap={2}>
          <Typography variant="subtitle1" fontWeight={600}>
            {mode === 'replace' ? 'Replace personal access token' : 'Personal Access Token'}
          </Typography>

          {mode === 'create' && (
            <Typography variant="body2" color="text.secondary">
              For users who can't install a GitHub App (org policy, personal accounts).
              Commits attribute to the PAT owner. Per-repo webhook is registered at
              repo provisioning. Required scopes: repo, admin:org, admin:repo_hook.
            </Typography>
          )}

          {topError && <Alert severity="error">{topError}</Alert>}

          <form onSubmit={submit}>
            <Stack gap={2}>
              <TextField
                fullWidth
                label="GitHub login / org"
                value={githubLogin}
                onChange={(e) => setGithubLogin(e.target.value)}
                error={Boolean(loginError)}
                helperText={loginError ?? 'The GitHub user or organization that owns the repos.'}
                disabled={submitting}
              />
              <TextField
                fullWidth
                label={formLabel}
                value={pat}
                onChange={(e) => setPat(e.target.value)}
                type={showPat ? 'text' : 'password'}
                error={Boolean(patError)}
                helperText={patError ?? 'Token is sent to git-service, validated, and stored encrypted in OpenBao.'}
                disabled={submitting}
                InputProps={{
                  endAdornment: (
                    <InputAdornment position="end">
                      <IconButton
                        onClick={() => setShowPat((v) => !v)}
                        edge="end"
                        size="small"
                        aria-label="toggle PAT visibility"
                      >
                        {showPat ? <EyeOff size={18} /> : <Eye size={18} />}
                      </IconButton>
                    </InputAdornment>
                  ),
                }}
              />
              <Stack direction="row" justifyContent="flex-end">
                <Button
                  variant="contained"
                  type="submit"
                  disabled={submitting}
                  startIcon={submitting ? <CircularProgress size={16} /> : null}
                >
                  {submitting ? 'Validating…' : buttonLabel}
                </Button>
              </Stack>
            </Stack>
          </form>
        </Stack>
      </CardContent>
    </Card>
  );
}
