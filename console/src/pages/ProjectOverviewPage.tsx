import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Box, CircularProgress, PageContent, Typography } from '@wso2/oxygen-ui';
import { ProjectStatusPolyline, type Stage } from '@asdlc/project-status';
import { api } from '../services/api';
import type { ComponentTask, ProjectSdlcPhase, ProjectStatus } from '../services/api';
import {
  projectArchitecturePath,
  projectRequirementsPath,
  projectTasksPath,
} from '../lib/paths';
import { buildProjectStages } from '../lib/buildProjectStages';
import ProjectPromptPage from './ProjectPromptPage';
import ProjectComponentsPage from './ProjectComponentsPage';

type Phase = ProjectSdlcPhase | null;

const ACTIVE_TASK_STATUSES: ReadonlySet<string> = new Set([
  'pending',
  'on_hold',
  'in_progress',
  'ready_for_review',
  'merged',
  'building',
]);

export default function ProjectOverviewPage() {
  const { orgId, projectId } = useParams();
  const navigate = useNavigate();
  const routeOrgId = orgId ?? 'default';
  const [phase, setPhase] = useState<Phase>(null);
  const [status, setStatus] = useState<ProjectStatus | undefined>();
  const [tasks, setTasks] = useState<ComponentTask[]>([]);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchAll = useCallback(async () => {
    if (!projectId) return;
    const [s, t] = await Promise.all([
      api.getProjectStatus(routeOrgId, projectId),
      api.listTasks(routeOrgId, projectId).catch(() => [] as ComponentTask[]),
    ]);
    setStatus(s);
    setPhase(s ? s.phase : 'no-repo');
    setTasks(t);
  }, [projectId, routeOrgId]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  // Poll while repo is cloning or any task is active in the pipeline.
  useEffect(() => {
    const cloning = phase === 'repo-cloning';
    const tasksActive = tasks.some((t) => ACTIVE_TASK_STATUSES.has(t.status));
    if (cloning || tasksActive) {
      const interval = cloning ? 3000 : 5000;
      pollRef.current = setInterval(fetchAll, interval);
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [phase, tasks, fetchAll]);

  const stages = useMemo(() => buildProjectStages(status, tasks), [status, tasks]);

  const handleStageClick = useCallback(
    (stage: Stage) => {
      if (!projectId) return;
      switch (stage.id) {
        case 'requirements':
          navigate(projectRequirementsPath(routeOrgId, projectId));
          break;
        case 'architecture':
          navigate(projectArchitecturePath(routeOrgId, projectId));
          break;
        case 'tasks':
          navigate(projectTasksPath(routeOrgId, projectId));
          break;
        // 'deployment' has no dedicated page yet — no-op.
      }
    },
    [navigate, projectId, routeOrgId],
  );

  if (phase === null) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <CircularProgress size={36} sx={{ mb: 2 }} />
          <Typography variant="body2" color="text.secondary">
            Loading project...
          </Typography>
        </Box>
      </PageContent>
    );
  }

  if (phase === 'repo-cloning') {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <CircularProgress size={48} sx={{ mb: 3 }} />
          <Typography variant="h6" color="text.secondary">
            Setting up repository...
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Cloning the Git repository. This may take a moment.
          </Typography>
        </Box>
      </PageContent>
    );
  }

  if (phase === 'no-repo') {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <Typography variant="h6" color="text.secondary" sx={{ mb: 1 }}>
            No Git repository associated
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Create a new project to use this platform.
          </Typography>
        </Box>
      </PageContent>
    );
  }

  if (phase === 'prompt') {
    return <ProjectPromptPage />;
  }

  return (
    <ProjectComponentsPage
      statusBanner={<ProjectStatusPolyline stages={stages} onStageClick={handleStageClick} />}
    />
  );
}
