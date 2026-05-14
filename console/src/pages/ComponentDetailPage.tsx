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
  Divider,
  Grid,
  PageContent,
  Skeleton,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { ArrowLeft, Clock } from '@wso2/oxygen-ui-icons-react';
import { formatDistanceToNow } from 'date-fns';
import { api } from '../services/api';
import type { ComponentDefinition, ComponentTask, Project, TaskStatus } from '../services/api';
import { organizationOverviewPath, projectOverviewPath } from '../lib/paths';

// ---------------------------------------------------------------------------
// Status helpers
// ---------------------------------------------------------------------------

const statusColors: Record<string, 'default' | 'primary' | 'warning' | 'success' | 'error'> = {
  created: 'default',
  pending: 'default',
  in_progress: 'warning',
  implementing: 'warning',
  completed: 'primary',
  deployed: 'success',
  done: 'success',
  failed: 'error',
};

const statusLabels: Record<string, string> = {
  created: 'Created',
  pending: 'Pending',
  in_progress: 'Implementing',
  implementing: 'Implementing',
  completed: 'Completed',
  deployed: 'Deployed',
  done: 'Ready',
  failed: 'Failed',
};

// ---------------------------------------------------------------------------
// Pipeline step rendering
// ---------------------------------------------------------------------------

type StepState = 'pending' | 'active' | 'completed' | 'failed';

interface PipelineStep {
  label: string;
  description: string;
  state: StepState;
}

function computePipelineSteps(task: ComponentTask): PipelineStep[] {
  const steps: PipelineStep[] = [];

  // Phase 0 lifecycle:
  //   pending → in_progress → ready_for_review → merged → building → deployed
  //                                            ↘ rejected
  //                                            ↘ failed (build)
  const status = task.status;

  const stateFor = (atOrPast: TaskStatus[], onlyAt: TaskStatus[] = []): StepState => {
    if (status === 'failed' && onlyAt.includes('building')) return 'failed';
    if (status === 'rejected' && onlyAt.includes('ready_for_review')) return 'failed';
    if (atOrPast.includes(status)) return 'completed';
    if (onlyAt.includes(status)) return 'active';
    return 'pending';
  };

  steps.push({
    label: 'Implementation',
    description:
      status === 'in_progress'
        ? 'Agent is working on the feature branch...'
        : status === 'pending'
          ? 'Waiting to start'
          : task.errorMessage && status === 'failed'
            ? task.errorMessage
            : 'Implementation complete',
    state: stateFor(['ready_for_review', 'merged', 'building', 'deployed'], ['in_progress']),
  });

  steps.push({
    label: 'Pull Request Ready',
    description:
      status === 'ready_for_review'
        ? 'Awaiting human review'
        : status === 'rejected'
          ? 'PR closed without merging'
          : (['merged', 'building', 'deployed'] as TaskStatus[]).includes(status)
            ? 'PR was approved and merged'
            : 'Not yet ready',
    state: stateFor(['merged', 'building', 'deployed'], ['ready_for_review']),
  });

  steps.push({
    label: 'Merged',
    description:
      status === 'merged'
        ? 'Default branch updated; build trigger pending'
        : (['building', 'deployed'] as TaskStatus[]).includes(status)
          ? 'Build was triggered for the merge SHA'
          : 'Not yet merged',
    state: stateFor(['building', 'deployed'], ['merged']),
  });

  steps.push({
    label: 'Build',
    description:
      status === 'building'
        ? 'Building container image...'
        : status === 'failed'
          ? task.errorMessage || 'Build failed'
          : status === 'deployed'
            ? 'Build succeeded'
            : 'Not started',
    state: stateFor(['deployed'], ['building']),
  });

  steps.push({
    label: 'Deploy',
    description:
      status === 'deployed'
        ? 'Auto-deploy in progress — check the Deploy page for endpoint status'
        : 'Not yet deployed',
    state: stateFor(['deployed']),
  });

  return steps;
}

function StepIcon({ state }: { state: StepState }) {
  switch (state) {
    case 'completed':
      return (
        <Box
          sx={{
            width: 24,
            height: 24,
            borderRadius: '50%',
            bgcolor: 'success.main',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            color: 'white',
            fontSize: 14,
            fontWeight: 700,
          }}
        >
          &#10003;
        </Box>
      );
    case 'active':
      return <CircularProgress size={24} />;
    case 'failed':
      return (
        <Box
          sx={{
            width: 24,
            height: 24,
            borderRadius: '50%',
            bgcolor: 'error.main',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            color: 'white',
            fontSize: 14,
            fontWeight: 700,
          }}
        >
          &#10005;
        </Box>
      );
    default:
      return (
        <Box
          sx={{
            width: 24,
            height: 24,
            borderRadius: '50%',
            bgcolor: 'action.disabledBackground',
          }}
        />
      );
  }
}

function PipelineStepper({ task }: { task: ComponentTask }) {
  const steps = computePipelineSteps(task);

  return (
    <Stack gap={0}>
      {steps.map((step, i) => (
        <Stack key={step.label} direction="row" gap={2} sx={{ position: 'relative' }}>
          {/* Vertical connector line */}
          {i < steps.length - 1 && (
            <Box
              sx={{
                position: 'absolute',
                left: 11,
                top: 28,
                bottom: -4,
                width: 2,
                bgcolor:
                  step.state === 'completed'
                    ? 'success.main'
                    : 'action.disabledBackground',
              }}
            />
          )}
          {/* Icon */}
          <Box sx={{ pt: 0.5, zIndex: 1 }}>
            <StepIcon state={step.state} />
          </Box>
          {/* Label + description */}
          <Box sx={{ pb: 2.5, flex: 1 }}>
            <Typography
              variant="body2"
              sx={{
                fontWeight: 600,
                color: step.state === 'pending' ? 'text.disabled' : 'text.primary',
              }}
            >
              {step.label}
            </Typography>
            <Typography
              variant="caption"
              sx={{
                color: step.state === 'failed' ? 'error.main' : 'text.secondary',
              }}
            >
              {step.description}
            </Typography>
          </Box>
        </Stack>
      ))}
    </Stack>
  );
}

// ---------------------------------------------------------------------------
// API Boundaries structured renderer
// ---------------------------------------------------------------------------

interface ParsedEndpoint {
  method: string;
  path: string;
  description: string;
}

interface ParsedBoundaries {
  endpoints: ParsedEndpoint[];
  port: string | null;
  protocol: string | null;
  extras: string[];
}

function parseApiBoundaries(raw: string): ParsedBoundaries {
  const endpoints: ParsedEndpoint[] = [];
  const extras: string[] = [];
  let port: string | null = null;
  let protocol: string | null = null;

  const lines = raw.split('\n');
  for (const line of lines) {
    const trimmed = line.trim();

    const endpointMatch = trimmed.match(/^-\s*`(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s+([^`]+)`\s*[-—]\s*(.+)$/);
    if (endpointMatch) {
      endpoints.push({
        method: endpointMatch[1],
        path: endpointMatch[2],
        description: endpointMatch[3],
      });
      continue;
    }

    const internalMatch = trimmed.match(/^-\s*`([^`]+)`\s*[-—]\s*(.+)$/);
    if (internalMatch) {
      endpoints.push({
        method: 'FN',
        path: internalMatch[1],
        description: internalMatch[2],
      });
      continue;
    }

    const portMatch = trimmed.match(/\*?\*?Port:?\*?\*?\s*(\d+)/i);
    if (portMatch) {
      port = portMatch[1];
      continue;
    }

    const protoMatch = trimmed.match(/\*?\*?Protocol:?\*?\*?\s*(.+)/i);
    if (protoMatch) {
      protocol = protoMatch[1].trim();
      continue;
    }

    const dbMatch = trimmed.match(/\*?\*?Database:?\*?\*?\s*(.+)/i);
    if (dbMatch) {
      extras.push(`Database: ${dbMatch[1].trim()}`);
      continue;
    }

    const commMatch = trimmed.match(/\*?\*?Communication:?\*?\*?\s*(.+)/i);
    if (commMatch) {
      extras.push(`Communication: ${commMatch[1].trim()}`);
      continue;
    }
  }

  return { endpoints, port, protocol, extras };
}

function methodColor(method: string): 'success' | 'primary' | 'warning' | 'error' | 'default' {
  switch (method) {
    case 'GET':
      return 'success';
    case 'POST':
      return 'primary';
    case 'PUT':
    case 'PATCH':
      return 'warning';
    case 'DELETE':
      return 'error';
    default:
      return 'default';
  }
}

function ApiBoundariesSection({ raw }: { raw: string }) {
  const parsed = parseApiBoundaries(raw);

  if (parsed.endpoints.length === 0 && !parsed.port && !parsed.protocol && parsed.extras.length === 0) {
    return (
      <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap', lineHeight: 1.7 }}>
        {raw}
      </Typography>
    );
  }

  return (
    <Stack gap={2}>
      {parsed.endpoints.length > 0 && (
        <Stack gap={1}>
          {parsed.endpoints.map((ep, i) => (
            <Stack
              key={i}
              direction="row"
              alignItems="center"
              gap={1.5}
              sx={{
                px: 2,
                py: 1,
                borderRadius: 1,
                bgcolor: 'action.hover',
              }}
            >
              <Chip
                label={ep.method}
                size="small"
                color={methodColor(ep.method)}
                variant="outlined"
                sx={{ fontFamily: 'monospace', fontWeight: 700, minWidth: 60 }}
              />
              <Typography
                variant="body2"
                sx={{ fontFamily: 'monospace', fontWeight: 600, minWidth: 0 }}
                noWrap
              >
                {ep.path}
              </Typography>
              <Typography variant="body2" color="text.secondary" sx={{ ml: 'auto', flexShrink: 0 }}>
                {ep.description}
              </Typography>
            </Stack>
          ))}
        </Stack>
      )}

      {(parsed.port || parsed.protocol || parsed.extras.length > 0) && (
        <Stack direction="row" gap={3} flexWrap="wrap" sx={{ mt: 0.5 }}>
          {parsed.port && (
            <Stack direction="row" gap={0.5} alignItems="center">
              <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>
                Port:
              </Typography>
              <Chip label={parsed.port} size="small" variant="outlined" />
            </Stack>
          )}
          {parsed.protocol && (
            <Stack direction="row" gap={0.5} alignItems="center">
              <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>
                Protocol:
              </Typography>
              <Chip label={parsed.protocol} size="small" variant="outlined" />
            </Stack>
          )}
          {parsed.extras.map((extra, i) => (
            <Typography key={i} variant="caption" color="text.secondary">
              {extra}
            </Typography>
          ))}
        </Stack>
      )}
    </Stack>
  );
}

// ---------------------------------------------------------------------------
// Interactions section
// ---------------------------------------------------------------------------

function InteractionsSection({ raw }: { raw: string }) {
  if (!raw) {
    return (
      <Typography variant="body2" color="text.secondary">
        No external interactions defined.
      </Typography>
    );
  }

  const items = raw
    .split(/\.\s+/)
    .map((s) => s.replace(/\.$/, '').trim())
    .filter(Boolean);

  if (items.length <= 1) {
    return (
      <Typography variant="body2" sx={{ lineHeight: 1.7 }}>
        {raw}
      </Typography>
    );
  }

  return (
    <Stack component="ul" gap={0.5} sx={{ m: 0, pl: 2.5 }}>
      {items.map((item, i) => (
        <Typography key={i} component="li" variant="body2" sx={{ lineHeight: 1.7 }}>
          {item}.
        </Typography>
      ))}
    </Stack>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export default function ComponentDetailPage() {
  const navigate = useNavigate();
  const { orgId, projectId, componentId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [project, setProject] = useState<Project | undefined>();
  const [component, setComponent] = useState<ComponentDefinition | undefined>();
  const [task, setTask] = useState<ComponentTask | undefined>();
  const [loading, setLoading] = useState(true);
  const intervalRef = useRef<ReturnType<typeof setInterval>>(undefined);

  const loadData = useCallback(async () => {
    if (!projectId || !componentId) return;
    const [p, c, tasks] = await Promise.all([
      api.getProject(routeOrgId, projectId),
      api.getComponent(routeOrgId, projectId, componentId),
      api.listTasks(routeOrgId, projectId),
    ]);
    setProject(p);
    setComponent(c);
    const matchingTask = tasks.find((t) => t.componentName === componentId);
    setTask(matchingTask);
    setLoading(false);
  }, [projectId, componentId, routeOrgId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Poll while pipeline is active
  useEffect(() => {
    if (!task) return;
    const isActive =
      task.status === 'pending' ||
      task.status === 'in_progress' ||
      task.status === 'ready_for_review' ||
      task.status === 'merged' ||
      task.status === 'building';

    if (isActive && projectId) {
      intervalRef.current = setInterval(async () => {
        const tasks = await api.listTasks(routeOrgId, projectId);
        const match = tasks.find((t) => t.componentName === componentId);
        if (match) setTask(match);
      }, 5000);
    }
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, [task?.status, projectId, componentId, routeOrgId]);

  if (loading) {
    return (
      <Box>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="rectangular" width="100%" height={300} sx={{ mt: 3, borderRadius: 1 }} />
      </Box>
    );
  }

  // If we have a task but no OC component yet, still show the page using task data
  const hasTask = !!task;
  const hasComponent = !!component;

  if (!project || (!hasComponent && !hasTask)) {
    return (
      <Box>
        <Typography variant="h5" color="error">
          Component not found
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

  const effectiveStatus = task?.status ?? component?.status ?? 'created';
  const componentName = task?.componentName ?? component?.name ?? componentId ?? '';
  const techStack = component?.techStack ?? '';

  return (
    <PageContent>
      <Box>
        {/* Back navigation */}
        <Button
          variant="text"
          startIcon={<ArrowLeft size={16} />}
          onClick={() => navigate(projectOverviewPath(routeOrgId, projectId!))}
          sx={{ mb: 2 }}
        >
          Back to Components
        </Button>

        {/* Header */}
        <Stack component="header" direction="row" alignItems="center" gap={2} sx={{ mb: 1 }}>
          <Avatar
            sx={{
              width: 56,
              height: 56,
              fontSize: 24,
              fontWeight: 700,
              bgcolor: 'text.primary',
              color: 'background.paper',
            }}
          >
            {componentName[0]?.toUpperCase() ?? 'C'}
          </Avatar>
          <Typography variant="h4" sx={{ fontWeight: 700 }}>
            {componentName}
          </Typography>
          <Stack direction="row" spacing={1} sx={{ ml: 'auto' }}>
            {techStack && <Chip label={techStack} variant="outlined" />}
            <Chip
              label={statusLabels[effectiveStatus] ?? effectiveStatus}
              color={statusColors[effectiveStatus] ?? 'default'}
            />
          </Stack>
        </Stack>

        {/* Subtitle */}
        <Stack direction="row" alignItems="center" gap={2} sx={{ ml: 9, mb: 4 }}>
          <Typography variant="body2" color="text.secondary">
            Component of {project.name}
          </Typography>
          <Stack direction="row" alignItems="center" gap={0.5}>
            <Clock size={14} opacity={0.5} />
            <Typography variant="body2" color="text.secondary">
              Updated{' '}
              {(task?.updatedAt ?? component?.updatedAt)
                ? formatDistanceToNow(new Date(task?.updatedAt ?? component?.updatedAt ?? ''), { addSuffix: true })
                : 'recently'}
            </Typography>
          </Stack>
        </Stack>

        <Grid container spacing={3}>
          {/* Left column */}
          <Grid size={{ xs: 12, md: 8 }}>
            {/* Overview */}
            <Card variant="outlined" sx={{ mb: 3 }}>
              <CardContent>
                <Typography variant="h6" sx={{ fontWeight: 600, mb: 2 }}>
                  Overview
                </Typography>
                <Stack gap={1.5}>
                  <Stack direction="row" gap={4}>
                    <Box>
                      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>
                        Type
                      </Typography>
                      <Typography variant="body2">{techStack}</Typography>
                    </Box>
                    <Box>
                      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>
                        Status
                      </Typography>
                      <Stack direction="row" alignItems="center" gap={1} sx={{ mt: 0.25 }}>
                        <Chip
                          label={statusLabels[effectiveStatus] ?? effectiveStatus}
                          color={statusColors[effectiveStatus] ?? 'default'}
                          size="small"
                        />
                      </Stack>
                    </Box>
                    {/* App path now lives in .asdlc/design.json — surfaced
                        per-component in the architecture page, not duplicated
                        on the task detail. */}
                    <Box>
                      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>
                        Created
                      </Typography>
                      <Typography variant="body2">
                        {(task?.createdAt ?? component?.createdAt)
                          ? new Date(task?.createdAt ?? component?.createdAt ?? '').toLocaleDateString()
                          : 'Unknown'}
                      </Typography>
                    </Box>
                  </Stack>
                </Stack>
              </CardContent>
            </Card>

            {/* Responsibilities — from OC component */}
            {component?.responsibilities && (
              <Card variant="outlined" sx={{ mb: 3 }}>
                <CardContent>
                  <Typography variant="h6" sx={{ fontWeight: 600, mb: 1.5 }}>
                    Responsibilities
                  </Typography>
                  <Typography variant="body1" sx={{ lineHeight: 1.8 }}>
                    {component.responsibilities}
                  </Typography>
                </CardContent>
              </Card>
            )}

            {/* API Boundaries (only if OC component exists) */}
            {component?.apiBoundaries && (
              <Card variant="outlined" sx={{ mb: 3 }}>
                <CardContent>
                  <Typography variant="h6" sx={{ fontWeight: 600, mb: 2 }}>
                    API Boundaries
                  </Typography>
                  <ApiBoundariesSection raw={component.apiBoundaries} />
                </CardContent>
              </Card>
            )}
          </Grid>

          {/* Right column */}
          <Grid size={{ xs: 12, md: 4 }}>
            {/* Interactions (only if OC component exists) */}
            {component?.interactions && (
              <Card variant="outlined" sx={{ mb: 3 }}>
                <CardContent>
                  <Typography variant="h6" sx={{ fontWeight: 600, mb: 1.5 }}>
                    Interactions
                  </Typography>
                  <InteractionsSection raw={component.interactions} />
                </CardContent>
              </Card>
            )}

            {/* Implementation Pipeline */}
            <Card variant="outlined" sx={{ mb: 3 }}>
              <CardContent>
                <Typography variant="h6" sx={{ fontWeight: 600, mb: 1.5 }}>
                  Implementation Pipeline
                </Typography>
                <Divider sx={{ mb: 2 }} />
                {task ? (
                  <PipelineStepper task={task} />
                ) : (
                  <Typography variant="body2" color="text.secondary">
                    No implementation task found for this component.
                  </Typography>
                )}
              </CardContent>
            </Card>
          </Grid>
        </Grid>
      </Box>
    </PageContent>
  );
}
