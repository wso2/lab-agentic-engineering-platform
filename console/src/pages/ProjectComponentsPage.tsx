import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Avatar,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Grid,
  LinearProgress,
  Skeleton,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { ArrowLeft, Clock, PlugZap } from '@wso2/oxygen-ui-icons-react';
import { formatDistanceToNow } from 'date-fns';
import { api } from '../services/api';
import type { ComponentDefinition, ComponentTask, Project } from '../services/api';
import { componentDetailPath, organizationOverviewPath, projectArchitecturePath } from '../lib/paths';

// ---------------------------------------------------------------------------
// Status helpers (compute display status from pipeline sub-statuses)
// ---------------------------------------------------------------------------

function computeDisplayStatus(task: ComponentTask): { label: string; color: 'default' | 'primary' | 'warning' | 'success' | 'error' | 'info' } {
  switch (task.status) {
    case 'failed':
      return { label: 'Failed', color: 'error' };
    case 'rejected':
      return { label: 'Rejected', color: 'error' };
    case 'deployed':
      return { label: 'Deployed', color: 'success' };
    case 'building':
      return { label: 'Building', color: 'primary' };
    case 'merged':
      return { label: 'Merged', color: 'primary' };
    case 'ready_for_review':
      return { label: 'Ready for Review', color: 'info' };
    case 'in_progress':
      return { label: 'Implementing', color: 'warning' };
    case 'pending':
      return { label: 'Pending', color: 'default' };
  }
  return { label: 'Unknown', color: 'default' };
}

const ocStatusConfig: Record<string, { label: string; color: 'default' | 'primary' | 'warning' | 'success' | 'error' }> = {
  created: { label: 'Created', color: 'default' },
};

// ---------------------------------------------------------------------------
// Component Card (with task pipeline status)
// ---------------------------------------------------------------------------

function ComponentCard({
  component,
  task,
  onClick,
}: {
  component?: ComponentDefinition;
  task?: ComponentTask;
  onClick: () => void;
}) {
  const displayStatus = task
    ? computeDisplayStatus(task)
    : ocStatusConfig[component?.status ?? ''] ?? { label: 'Created', color: 'default' as const };

  const isActive =
    task?.status === 'in_progress' ||
    task?.status === 'ready_for_review' ||
    task?.status === 'merged' ||
    task?.status === 'building';

  const name = component?.name ?? task?.componentName ?? '';
  const techStack = component?.techStack ?? '';
  const summary = component?.responsibilities ?? '';
  const updatedAt = task?.updatedAt ?? component?.updatedAt;

  return (
    <Card
      variant="outlined"
      sx={{
        cursor: 'pointer',
        transition: 'box-shadow 0.2s, border-color 0.2s',
        '&:hover': { boxShadow: 4, borderColor: 'primary.main' },
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        position: 'relative',
        overflow: 'hidden',
      }}
      onClick={onClick}
    >
      {isActive && (
        <LinearProgress
          sx={{ position: 'absolute', top: 0, left: 0, right: 0 }}
        />
      )}
      <CardContent sx={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
        {/* Header */}
        <Stack direction="row" alignItems="center" gap={1.5} sx={{ mb: 1.5 }}>
          <Avatar
            sx={{
              width: 40,
              height: 40,
              fontSize: 16,
              fontWeight: 700,
              bgcolor: 'text.primary',
              color: 'background.paper',
            }}
          >
            {name[0]?.toUpperCase() ?? 'C'}
          </Avatar>
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Typography variant="subtitle1" sx={{ fontWeight: 700, lineHeight: 1.3 }} noWrap>
              {name}
            </Typography>
            <Typography variant="caption" color="text.secondary" noWrap>
              {techStack}
            </Typography>
          </Box>
          <Chip
            label={displayStatus.label}
            color={displayStatus.color}
            size="small"
            variant="outlined"
          />
        </Stack>

        {/* Summary */}
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{
            mb: 2,
            flex: 1,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            display: '-webkit-box',
            WebkitLineClamp: 2,
            WebkitBoxOrient: 'vertical',
            lineHeight: 1.6,
          }}
        >
          {summary}
        </Typography>

        {/* Footer */}
        <Stack direction="row" alignItems="center" gap={0.5}>
          {isActive && (
            <CircularProgress size={12} sx={{ mr: 0.5 }} />
          )}
          <Clock size={14} opacity={0.5} />
          <Typography variant="caption" color="text.secondary">
            {updatedAt
              ? formatDistanceToNow(new Date(updatedAt), { addSuffix: true })
              : 'just now'}
          </Typography>
        </Stack>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export default function ProjectComponentsPage() {
  const navigate = useNavigate();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [project, setProject] = useState<Project | undefined>();
  const [components, setComponents] = useState<ComponentDefinition[]>([]);
  const [tasks, setTasks] = useState<ComponentTask[]>([]);
  const [loading, setLoading] = useState(true);
  const intervalRef = useRef<ReturnType<typeof setInterval>>(undefined);

  const loadData = useCallback(async () => {
    if (!projectId) return;
    const [p, c, t] = await Promise.all([
      api.getProject(routeOrgId, projectId),
      api.listComponents(routeOrgId, projectId),
      api.listTasks(routeOrgId, projectId),
    ]);
    setProject(p);
    setComponents(c);
    setTasks(t);
    setLoading(false);
  }, [projectId, routeOrgId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Poll while any task is active in the pipeline
  useEffect(() => {
    const hasActive = tasks.some((t) =>
      t.status === 'pending' ||
      t.status === 'in_progress' ||
      t.status === 'ready_for_review' ||
      t.status === 'merged' ||
      t.status === 'building'
    );
    if (hasActive) {
      intervalRef.current = setInterval(async () => {
        if (!projectId) return;
        const t = await api.listTasks(routeOrgId, projectId);
        setTasks(t);
      }, 5000);
    }
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, [tasks, projectId, routeOrgId]);

  // Build maps: componentName → task, componentName → OC component
  const taskMap = new Map<string, ComponentTask>();
  for (const t of tasks) {
    taskMap.set(t.componentName, t);
  }
  const componentMap = new Map<string, ComponentDefinition>();
  for (const c of components) {
    componentMap.set(c.name, c);
  }

  // Merge: show cards for all unique component names from both tasks and OC components.
  // Tasks take priority since OC components may not exist yet.
  const allNames = new Set<string>();
  for (const t of tasks) allNames.add(t.componentName);
  for (const c of components) allNames.add(c.name);
  const cardEntries = Array.from(allNames).map((name) => ({
    name,
    task: taskMap.get(name),
    component: componentMap.get(name),
  }));

  if (loading) {
    return (
      <Box>
        <Skeleton variant="text" width="40%" height={40} />
        <Grid container spacing={2} sx={{ mt: 3 }}>
          {Array.from({ length: 4 }).map((_, i) => (
            <Grid key={i} size={{ xs: 12, sm: 6, lg: 4 }}>
              <Skeleton variant="rounded" height={160} />
            </Grid>
          ))}
        </Grid>
      </Box>
    );
  }

  if (!project) {
    return (
      <Box>
        <Typography variant="h5" color="error">
          Project not found
        </Typography>
        <Button
          variant="text"
          startIcon={<ArrowLeft size={16} />}
          onClick={() => navigate(organizationOverviewPath(routeOrgId))}
          sx={{ mt: 2 }}
        >
          Back to Projects
        </Button>
      </Box>
    );
  }

  return (
    <Box>
      {/* Page header */}
      <Stack direction="row" alignItems="center" gap={2} sx={{ mb: 0.5 }}>
        <Typography variant="h4" fontWeight={700}>
          {project.name}
        </Typography>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        {cardEntries.length} component{cardEntries.length !== 1 ? 's' : ''}
      </Typography>

      {cardEntries.length === 0 ? (
        <Box
          sx={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            py: 8,
          }}
        >
          <PlugZap size={48} opacity={0.3} />
          <Typography variant="h6" color="text.secondary" sx={{ mt: 2, mb: 1 }}>
            No components yet
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
            Approve the architecture to create components.
          </Typography>
          <Button
            variant="contained"
            onClick={() => navigate(projectArchitecturePath(routeOrgId, projectId!))}
          >
            Go to Architecture
          </Button>
        </Box>
      ) : (
        <Grid container spacing={2}>
          {cardEntries.map((entry) => (
            <Grid key={entry.name} size={{ xs: 12, sm: 6, lg: 4 }}>
              <ComponentCard
                component={entry.component}
                task={entry.task}
                onClick={() => navigate(componentDetailPath(routeOrgId, projectId!, entry.name))}
              />
            </Grid>
          ))}
        </Grid>
      )}
    </Box>
  );
}
