import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  CircularProgress,
  Typography,
} from '@wso2/oxygen-ui';
import { api } from '../services/api';
import type { ProjectSdlcPhase } from '../services/api';
import {
  projectPromptPath,
  projectRequirementsPath,
  projectArchitecturePath,
  projectTasksPath,
  projectComponentsPath,
} from '../lib/paths';

export default function ProjectOverviewPage() {
  const navigate = useNavigate();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';
  const [phase, setPhase] = useState<ProjectSdlcPhase | null>(null);
  const [loading, setLoading] = useState(true);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchStatus = useCallback(async () => {
    if (!projectId) return;
    const status = await api.getProjectStatus(routeOrgId, projectId);
    if (!status) {
      setPhase('no-repo');
      setLoading(false);
      return;
    }

    setPhase(status.phase);
    setLoading(false);

    switch (status.phase) {
      case 'prompt':
        navigate(projectPromptPath(routeOrgId, projectId), { replace: true });
        break;
      case 'spec':
        navigate(projectRequirementsPath(routeOrgId, projectId), { replace: true });
        break;
      case 'architecture':
        navigate(projectArchitecturePath(routeOrgId, projectId), { replace: true });
        break;
      case 'tasks':
        navigate(projectTasksPath(routeOrgId, projectId), { replace: true });
        break;
      case 'components':
        navigate(projectComponentsPath(routeOrgId, projectId), { replace: true });
        break;
      // "no-repo" and "repo-cloning" render inline — no redirect
    }
  }, [projectId, routeOrgId, navigate]);

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

  if (loading) {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
        <CircularProgress size={48} sx={{ mb: 3 }} />
        <Typography variant="h6" color="text.secondary">
          Loading project...
        </Typography>
      </Box>
    );
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

  // "no-repo" phase
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
