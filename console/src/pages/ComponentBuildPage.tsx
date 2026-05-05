import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  IconButton,
  Skeleton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@wso2/oxygen-ui';
import { Package, RefreshCw, X } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import type { ComponentDefinition } from '../services/api';
import type { Build, BuildLogs, BuildStatus } from '../services/api/types';

function statusColor(status: BuildStatus): 'success' | 'error' | 'warning' | 'default' {
  switch (status) {
    case 'Succeeded':
    case 'Completed':
    case 'WorkflowSucceeded':
      return 'success';
    case 'Failed':
    case 'WorkflowFailed':
      return 'error';
    case 'Running':
    case 'Pending':
    case 'WorkflowPending':
      return 'warning';
    default:
      return 'default';
  }
}

function statusLabel(status: BuildStatus): string {
  switch (status) {
    case 'WorkflowSucceeded': return 'Succeeded';
    case 'WorkflowFailed': return 'Failed';
    case 'WorkflowPending': return 'Pending';
    default: return status;
  }
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

export default function ComponentBuildPage() {
  const { orgId, projectId, componentId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [component, setComponent] = useState<ComponentDefinition | undefined>();
  const [builds, setBuilds] = useState<Build[]>([]);
  const [loading, setLoading] = useState(true);
  const [triggering, setTriggering] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Build logs drawer state
  const [selectedBuild, setSelectedBuild] = useState<Build | null>(null);
  const [buildLogs, setBuildLogs] = useState<BuildLogs | null>(null);
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsError, setLogsError] = useState<string | null>(null);

  const loadData = useCallback(async () => {
    if (!projectId || !componentId) return;
    const [c, b] = await Promise.all([
      api.getComponent(routeOrgId, projectId, componentId),
      api.listBuilds(routeOrgId, projectId, componentId),
    ]);
    setComponent(c);
    setBuilds(b);
    setLoading(false);
  }, [projectId, componentId, routeOrgId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Poll active builds every 5s
  useEffect(() => {
    const hasActive = builds.some((b) => b.status === 'Pending' || b.status === 'Running');
    if (hasActive && projectId && componentId) {
      pollRef.current = setInterval(async () => {
        const updated = await api.listBuilds(routeOrgId, projectId, componentId);
        setBuilds(updated);
      }, 5000);
    }
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [builds, routeOrgId, projectId, componentId]);

  const handleTriggerBuild = async () => {
    if (!projectId || !componentId) return;
    setTriggering(true);
    await api.triggerBuild(routeOrgId, projectId, componentId);
    const updated = await api.listBuilds(routeOrgId, projectId, componentId);
    setBuilds(updated);
    setTriggering(false);
  };

  const handleRefresh = async () => {
    if (!projectId || !componentId) return;
    const updated = await api.listBuilds(routeOrgId, projectId, componentId);
    setBuilds(updated);
  };

  const handleBuildClick = async (build: Build) => {
    if (!projectId || !componentId) return;
    setSelectedBuild(build);
    setBuildLogs(null);
    setLogsError(null);
    setLogsLoading(true);
    try {
      const logs = await api.getBuildLogs(routeOrgId, projectId, componentId, build.name);
      if (logs) {
        setBuildLogs(logs);
      } else {
        setLogsError('Build logs are not available');
      }
    } catch {
      setLogsError('Failed to fetch build logs');
    } finally {
      setLogsLoading(false);
    }
  };

  const handleCloseDrawer = () => {
    setSelectedBuild(null);
    setBuildLogs(null);
    setLogsError(null);
  };

  if (loading) {
    return (
      <Box>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="rectangular" width="100%" height={200} sx={{ mt: 3, borderRadius: 1 }} />
      </Box>
    );
  }

  if (!component) {
    return (
      <Box>
        <Typography variant="h5" color="error">
          Component not found
        </Typography>
      </Box>
    );
  }

  return (
    <Box>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 3 }}>
        <Box>
          <Typography variant="h4" sx={{ fontWeight: 700, mb: 0.5 }}>
            {component.name}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Build
          </Typography>
        </Box>
        <Stack direction="row" gap={1}>
          <Button
            variant="outlined"
            size="small"
            startIcon={<RefreshCw />}
            onClick={handleRefresh}
          >
            Refresh
          </Button>
          <Button
            variant="contained"
            size="small"
            onClick={handleTriggerBuild}
            disabled={triggering}
          >
            {triggering ? 'Triggering...' : 'Trigger Build'}
          </Button>
        </Stack>
      </Stack>

      {builds.length === 0 ? (
        <Card variant="outlined">
          <CardContent>
            <Stack alignItems="center" gap={2} sx={{ py: 6 }}>
              <Package size={48} opacity={0.4} />
              <Typography variant="h6" color="text.secondary">
                No builds yet
              </Typography>
              <Typography variant="body2" color="text.secondary">
                Trigger a build to get started.
              </Typography>
            </Stack>
          </CardContent>
        </Card>
      ) : (
        <TableContainer>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Build Name</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Started</TableCell>
                <TableCell>Image</TableCell>
                <TableCell>Commit</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {builds.map((build) => (
                <TableRow
                  key={build.name}
                  onClick={() => handleBuildClick(build)}
                  sx={{ cursor: 'pointer', '&:hover': { backgroundColor: 'action.hover' } }}
                >
                  <TableCell>
                    <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                      {build.name}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Chip
                      label={statusLabel(build.status as BuildStatus)}
                      color={statusColor(build.status as BuildStatus)}
                      size="small"
                    />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">
                      {timeAgo(build.startedAt)}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography
                      variant="body2"
                      sx={{ fontFamily: 'monospace', maxWidth: 300, overflow: 'hidden', textOverflow: 'ellipsis' }}
                    >
                      {build.image || '—'}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                      {build.commit ? build.commit.substring(0, 8) : '—'}
                    </Typography>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Build Logs Drawer */}
      {selectedBuild && (
        <>
          <Box
            onClick={handleCloseDrawer}
            sx={{
              position: 'fixed',
              top: 0,
              left: 0,
              width: '100%',
              height: '100vh',
              backgroundColor: 'rgba(0,0,0,0.3)',
              zIndex: 1299,
            }}
          />
          <Box
            sx={{
              position: 'fixed',
              top: 0,
              right: 0,
              width: { xs: '100%', md: '50%', lg: '40%' },
              height: '100vh',
              backgroundColor: 'background.paper',
              boxShadow: '-4px 0 20px rgba(0,0,0,0.15)',
              zIndex: 1300,
              display: 'flex',
              flexDirection: 'column',
              overflow: 'hidden',
            }}
          >
            <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ px: 2, py: 1.5 }}>
              <Box>
                <Typography variant="h6" sx={{ fontWeight: 600 }}>Build Logs</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ fontFamily: 'monospace' }}>
                  {selectedBuild.name}
                </Typography>
              </Box>
              <IconButton size="small" onClick={handleCloseDrawer}>
                <X size={16} />
              </IconButton>
            </Stack>

            <Stack direction="row" gap={1} alignItems="center" sx={{ px: 2, pb: 1.5 }}>
              <Chip
                label={statusLabel(selectedBuild.status as BuildStatus)}
                color={statusColor(selectedBuild.status as BuildStatus)}
                size="small"
              />
              <Typography variant="body2" color="text.secondary">
                {timeAgo(selectedBuild.startedAt)}
              </Typography>
            </Stack>

            <Box
              sx={{
                flex: 1,
                overflow: 'auto',
                backgroundColor: '#1e1e1e',
                mx: 2,
                mb: 2,
                borderRadius: 1,
                p: 2,
              }}
            >
              {logsLoading && (
                <Stack alignItems="center" sx={{ py: 4 }}>
                  <CircularProgress size={32} />
                  <Typography variant="body2" sx={{ mt: 1, color: '#ccc' }}>
                    Loading logs...
                  </Typography>
                </Stack>
              )}
              {logsError && (
                <Typography variant="body2" sx={{ color: '#ff6b6b' }}>
                  {logsError}
                </Typography>
              )}
              {buildLogs && buildLogs.logs.length === 0 && (
                <Typography variant="body2" sx={{ color: '#999' }}>
                  No log entries found.
                </Typography>
              )}
              {buildLogs && buildLogs.logs.length > 0 && (
                <Box
                  component="pre"
                  sx={{
                    fontFamily: 'monospace',
                    fontSize: '0.8rem',
                    lineHeight: 1.6,
                    color: '#d4d4d4',
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-all',
                    m: 0,
                  }}
                >
                  {buildLogs.logs.map((entry, i) => (
                    <Box key={i} component="span" sx={{ display: 'block' }}>
                      <Box component="span" sx={{ color: '#6a9955' }}>
                        {entry.timestamp ? new Date(entry.timestamp).toLocaleTimeString() : ''}
                      </Box>
                      {' '}
                      {entry.log}
                    </Box>
                  ))}
                </Box>
              )}
            </Box>
          </Box>
        </>
      )}
    </Box>
  );
}
