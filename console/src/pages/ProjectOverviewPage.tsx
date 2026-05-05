import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Box, CircularProgress, Typography } from '@wso2/oxygen-ui';
import { api } from '../services/api';
import type { ProjectSdlcPhase } from '../services/api';
import ProjectPromptPage from './ProjectPromptPage';
import ProjectComponentsPage from './ProjectComponentsPage';

type Phase = ProjectSdlcPhase | null;

export default function ProjectOverviewPage() {
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';
  const [phase, setPhase] = useState<Phase>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchStatus = useCallback(async () => {
    if (!projectId) return;
    const status = await api.getProjectStatus(routeOrgId, projectId);
    setPhase(status ? status.phase : 'no-repo');
  }, [projectId, routeOrgId]);

  useEffect(() => {
    fetchStatus();
  }, [fetchStatus]);

  // Poll while repo is cloning
  useEffect(() => {
    if (phase === 'repo-cloning') {
      pollRef.current = setInterval(fetchStatus, 3000);
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [phase, fetchStatus]);

  if (phase === null) {
    return null;
  }

  if (phase === 'repo-cloning') {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
        <CircularProgress size={48} sx={{ mb: 3 }} />
        <Typography variant="h6" color="text.secondary">
          Setting up repository...
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          Cloning the Git repository. This may take a moment.
        </Typography>
      </Box>
    );
  }

  if (phase === 'no-repo') {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
        <Typography variant="h6" color="text.secondary" sx={{ mb: 1 }}>
          No Git repository associated
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Create a new project to use this platform.
        </Typography>
      </Box>
    );
  }

  return phase === 'prompt' ? <ProjectPromptPage /> : <ProjectComponentsPage />;
}
