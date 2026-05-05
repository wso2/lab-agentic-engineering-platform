import { useState } from 'react';
import { Button, CircularProgress } from '@wso2/oxygen-ui';
import { Github } from '@wso2/oxygen-ui-icons-react';
import { orgGithubApi } from '../services/api/orgGithub';

interface Props {
  orgHandle: string;
}

/**
 * ConnectAppButton — App-mode connect entry.
 *
 * On click: POST /github/connect/start → receive a GitHub OAuth authorize
 * URL → full-page redirect. The connect-state JWT (15-min TTL) carries
 * ocOrgId + actor through OAuth. The callback exchanges the code for a
 * user-token, intersects /user/installations with our App's installs,
 * and either binds directly (1 candidate), redirects to install
 * (0 candidates), or sends to the picker (2+ candidates).
 *
 * Full-page redirect (not popup) — GitHub's auth pages can't be iframed
 * and a popup adds focus/state-loss complexity.
 */
export default function ConnectAppButton({ orgHandle }: Props) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleClick = async () => {
    setLoading(true);
    setError(null);
    try {
      const { authorizeUrl } = await orgGithubApi.startConnect(orgHandle);
      window.location.assign(authorizeUrl);
    } catch (err) {
      const e = err as Error;
      setError(e.message || 'Could not start App connect');
      setLoading(false);
    }
  };

  return (
    <>
      <Button
        variant="contained"
        startIcon={loading ? <CircularProgress size={16} /> : <Github size={18} />}
        onClick={handleClick}
        disabled={loading}
        size="large"
      >
        {loading ? 'Starting…' : 'Connect GitHub App'}
      </Button>
      {error && (
        <div style={{ color: 'red', marginTop: 8, fontSize: 13 }}>{error}</div>
      )}
    </>
  );
}
