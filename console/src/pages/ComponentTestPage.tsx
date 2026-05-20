import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  PageContent,
  Skeleton,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { FlaskConical, RefreshCw } from '@wso2/oxygen-ui-icons-react';
import yaml from 'js-yaml';
import SwaggerUI from 'swagger-ui-react';
import 'swagger-ui-react/swagger-ui.css';
import { api } from '../services/api';
import type { ComponentOpenAPI } from '../services/api';
import type { Deployment } from '../services/api/types';

type SpecResult =
  | ComponentOpenAPI
  | { error: 'not-service'; componentType: string }
  | { error: 'not-found' };

function isSpec(r: SpecResult | undefined): r is ComponentOpenAPI {
  return !!r && !('error' in r);
}

export default function ComponentTestPage() {
  const { orgId, projectId, componentId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [specResult, setSpecResult] = useState<SpecResult | undefined>();
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [loading, setLoading] = useState(true);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const loadData = useCallback(async () => {
    if (!projectId || !componentId) return;
    const [s, d] = await Promise.all([
      api.getComponentOpenAPI(routeOrgId, projectId, componentId),
      api.listDeployments(routeOrgId, projectId, componentId),
    ]);
    setSpecResult(s);
    setDeployments(d);
    setLoading(false);
  }, [routeOrgId, projectId, componentId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Poll while the spec is present but no live deployment has appeared yet —
  // the user might be staring at this tab waiting for OC's autoDeploy to fan
  // out. Same cadence as the Deploy page (5s).
  useEffect(() => {
    if (isSpec(specResult) && deployments.length === 0 && projectId && componentId) {
      pollRef.current = setInterval(async () => {
        const d = await api.listDeployments(routeOrgId, projectId, componentId);
        setDeployments(d);
      }, 5000);
    }
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [specResult, deployments.length, routeOrgId, projectId, componentId]);

  // Active deployment + computed swagger spec with `servers` rewritten to the
  // live endpoint URL. We only ever have one environment today; first item wins.
  const activeDeployment = deployments[0];
  const swaggerSpec = useMemo(() => {
    if (!isSpec(specResult) || !activeDeployment?.endpointUrl) return undefined;
    try {
      const doc = yaml.load(specResult.spec) as Record<string, unknown>;
      if (!doc || typeof doc !== 'object') return null;
      return { ...doc, servers: [{ url: activeDeployment.endpointUrl }] };
    } catch {
      return null; // YAML parse error
    }
  }, [specResult, activeDeployment]);

  // swagger-ui invokes the deployed endpoint directly. CORS is enabled on
  // the service ClusterComponentType's HTTPRoute so the browser preflight
  // succeeds without a server-side proxy. Nothing to intercept today; the
  // hook is retained as a no-op for future per-request header injection.

  if (loading) {
    return (
      <PageContent>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="rectangular" width="100%" height={400} sx={{ mt: 3, borderRadius: 1 }} />
      </PageContent>
    );
  }

  const renderEmptyState = (heading: string, body: string) => (
    <Card variant="outlined">
      <CardContent>
        <Stack alignItems="center" gap={2} sx={{ py: 6, textAlign: 'center' }}>
          <FlaskConical size={48} opacity={0.4} />
          <Typography variant="h6" color="text.secondary">{heading}</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ maxWidth: 480 }}>
            {body}
          </Typography>
        </Stack>
      </CardContent>
    </Card>
  );

  return (
    <PageContent>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 3 }}>
        <Box>
          <Typography variant="h4" sx={{ fontWeight: 700, mb: 0.5 }}>
            {componentId}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Test
          </Typography>
        </Box>
        <Button
          variant="outlined"
          size="small"
          startIcon={<RefreshCw />}
          onClick={loadData}
        >
          Refresh
        </Button>
      </Stack>

      {specResult && 'error' in specResult && specResult.error === 'not-service' && (
        renderEmptyState(
          'Not testable here',
          `The Test tab is only available for "service" components. This one is a "${specResult.componentType}" — try running it locally or reviewing its overview tab.`,
        )
      )}

      {specResult && 'error' in specResult && specResult.error === 'not-found' && (
        renderEmptyState(
          'No API spec yet',
          'The architecture phase must complete (with a saved design) before this page is usable.',
        )
      )}

      {isSpec(specResult) && !activeDeployment && (
        renderEmptyState(
          'No live endpoint yet',
          'Once the build completes, OpenChoreo auto-deploys this component and the Test tab picks it up. This page refreshes every few seconds.',
        )
      )}

      {isSpec(specResult) && activeDeployment && swaggerSpec === null && (
        renderEmptyState(
          'OpenAPI spec is invalid',
          'The generated OpenAPI YAML failed to parse. Regenerate the design from the Architecture tab to fix.',
        )
      )}

      {isSpec(specResult) && activeDeployment?.endpointUrl && swaggerSpec && (
        <Card variant="outlined">
          <CardContent>
            <Stack direction="row" alignItems="center" gap={1} sx={{ mb: 0.5 }}>
              <Typography variant="body2" color="text.secondary">Endpoint:</Typography>
              <Typography
                component="a"
                href={activeDeployment.endpointUrl}
                target="_blank"
                rel="noopener"
                variant="body2"
                sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
              >
                {activeDeployment.endpointUrl}
              </Typography>
            </Stack>
            <Box
              sx={{
                // Tame swagger-ui's heavy header / model styling next to Oxygen UI.
                '& .swagger-ui .topbar': { display: 'none' },
                '& .swagger-ui .info': { margin: '0 0 16px 0' },
                '& .swagger-ui .info hgroup.main': { margin: 0 },
                '& .swagger-ui .scheme-container': { display: 'none' },
              }}
            >
              <SwaggerUI
                spec={swaggerSpec}
                docExpansion="list"
                tryItOutEnabled
                deepLinking={false}
              />
            </Box>
          </CardContent>
        </Card>
      )}
    </PageContent>
  );
}
