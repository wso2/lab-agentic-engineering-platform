import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  PageContent,
  Skeleton,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { Rocket, RefreshCw } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import type { ComponentDefinition } from '../services/api';
import type { Build, Deployment } from '../services/api/types';

function statusColor(status: string): 'success' | 'error' | 'warning' | 'default' {
  if (status === 'Ready') return 'success';
  if (status.includes('Failed') || status.includes('Error')) return 'error';
  if (status.includes('Progressing') || status === 'ReleaseSynced') return 'warning';
  return 'default';
}

function timeAgo(iso: string): string {
  if (!iso) return '';
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export default function ComponentDeployPage() {
  const { orgId, projectId, componentId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [component, setComponent] = useState<ComponentDefinition | undefined>();
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [builds, setBuilds] = useState<Build[]>([]);
  const [loading, setLoading] = useState(true);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const hasSuccessfulBuild = builds.some(
    (b) => b.status === 'Succeeded' || b.status === 'WorkflowSucceeded' || b.status === 'Completed',
  );
  // Auto-deploy is in flight when a build has succeeded but OC has not yet
  // materialised a ReleaseBinding for this component. OC's Component
  // controller normally reacts within seconds of the build's
  // generate-workload-cr step POSTing the Workload, so this state is brief.
  const awaitingAutoDeploy = hasSuccessfulBuild && deployments.length === 0;

  const loadData = useCallback(async () => {
    if (!projectId || !componentId) return;
    const [c, d, b] = await Promise.all([
      api.getComponent(routeOrgId, projectId, componentId),
      api.listDeployments(routeOrgId, projectId, componentId),
      api.listBuilds(routeOrgId, projectId, componentId),
    ]);
    setComponent(c);
    setDeployments(d);
    setBuilds(b);
    setLoading(false);
  }, [projectId, componentId, routeOrgId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Poll while we are either waiting for autoDeploy to fan out into a
  // ReleaseBinding, or while a binding is in a non-terminal status. The
  // BFF doesn't push these — the page just polls OC via ListDeployments.
  useEffect(() => {
    const hasActiveBinding = deployments.some(
      (d) => d.status && !d.status.includes('Ready') && !d.status.includes('Failed'),
    );
    const shouldPoll = awaitingAutoDeploy || hasActiveBinding;
    if (shouldPoll && projectId && componentId) {
      pollRef.current = setInterval(async () => {
        const updated = await api.listDeployments(routeOrgId, projectId, componentId);
        setDeployments(updated);
      }, 5000);
    }
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [deployments, awaitingAutoDeploy, routeOrgId, projectId, componentId]);

  const handleRefresh = async () => {
    if (!projectId || !componentId) return;
    const updated = await api.listDeployments(routeOrgId, projectId, componentId);
    setDeployments(updated);
  };

  if (loading) {
    return (
      <PageContent>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="rectangular" width="100%" height={200} sx={{ mt: 3, borderRadius: 1 }} />
      </PageContent>
    );
  }

  if (!component) {
    return (
      <PageContent>
        <Typography variant="h5" color="error">
          Component not found
        </Typography>
      </PageContent>
    );
  }

  return (
    <PageContent>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 3 }}>
        <Box>
          <Typography variant="h4" sx={{ fontWeight: 700, mb: 0.5 }}>
            {component.name}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Deploy
          </Typography>
        </Box>
        <Button
          variant="outlined"
          size="small"
          startIcon={<RefreshCw />}
          onClick={handleRefresh}
        >
          Refresh
        </Button>
      </Stack>

      {deployments.length === 0 ? (
        <Card variant="outlined">
          <CardContent>
            <Stack alignItems="center" gap={2} sx={{ py: 6, textAlign: 'center' }}>
              <Rocket size={48} opacity={0.4} />
              {awaitingAutoDeploy ? (
                <>
                  <Typography variant="h6" color="text.secondary">
                    Deploying…
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    The build finished and OpenChoreo is rolling out the new release.
                    This page will pick up the deployment automatically.
                  </Typography>
                </>
              ) : (
                <>
                  <Typography variant="h6" color="text.secondary">
                    No deployments yet
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    Trigger a build from the Build page. Once the build succeeds,
                    OpenChoreo will deploy this component automatically.
                  </Typography>
                </>
              )}
            </Stack>
          </CardContent>
        </Card>
      ) : (
        <Stack gap={2}>
          {deployments.map((dep) => (
            <Card key={dep.name} variant="outlined">
              <CardContent>
                <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 2 }}>
                  <Typography variant="h6" sx={{ fontWeight: 600 }}>
                    {dep.environment}
                  </Typography>
                  <Chip
                    label={dep.status || 'Deploying'}
                    color={statusColor(dep.status)}
                    size="small"
                  />
                </Stack>
                <Stack gap={1.5}>
                  {dep.endpointUrl && (
                    <Box>
                      <Typography variant="body2" color="text.secondary" sx={{ mb: 0.5 }}>
                        Endpoint URL:
                      </Typography>
                      <Typography
                        component="a"
                        href={dep.endpointUrl}
                        target="_blank"
                        rel="noopener"
                        variant="body2"
                        sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
                      >
                        {dep.endpointUrl}
                      </Typography>
                    </Box>
                  )}
                  <Typography variant="body2" color="text.secondary">
                    Release: <code>{dep.releaseName}</code>
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    Created: {timeAgo(dep.createdAt)}
                  </Typography>
                </Stack>
              </CardContent>
            </Card>
          ))}
        </Stack>
      )}
    </PageContent>
  );
}
