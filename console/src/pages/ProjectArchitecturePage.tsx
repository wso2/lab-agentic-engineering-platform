import { lazy, memo, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Divider,
  Grid,
  PageContent,
  Stack,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import { ChevronRight, RefreshCcw, Rocket } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import type { ArtifactVersion, Design, DesignComponent } from '../services/api';
import { projectTasksPath } from '../lib/paths';
import VersionSelector from '../components/VersionSelector';
import LineageLabel from '../components/LineageLabel';

// ---------------------------------------------------------------------------
// Cell Diagram (lazy-loaded from @wso2/cell-diagram)
// ---------------------------------------------------------------------------

const CellView = lazy(() =>
  import('@wso2/cell-diagram').then((module) => ({
    default: module.CellDiagram,
  }))
);


type ChangeType = 'added' | 'preserved' | 'removed';

function buildProjectModel(
  components: DesignComponent[],
  statuses?: Record<string, ChangeType>,
): import('@wso2/cell-diagram').Project {
  const typeMap: Record<string, import('@wso2/cell-diagram').ComponentType> = {
    'web-app': 'web-app' as import('@wso2/cell-diagram').ComponentType,
    service: 'service' as import('@wso2/cell-diagram').ComponentType,
    'scheduled-task': 'scheduled-task' as import('@wso2/cell-diagram').ComponentType,
  };

  // Removed components are filtered out of the diagram — it previews the
  // post-publish state. Added/preserved components render as usual.
  const visible = statuses
    ? components.filter((c) => statuses[c.name] !== 'removed')
    : components;

  return {
    id: 'project',
    name: 'Architecture',
    modelVersion: '0.2.0',
    components: visible.map((comp) => ({
      id: comp.name,
      label: comp.name,
      version: '1.0.0',
      type: typeMap[comp.componentType] ?? ('service' as import('@wso2/cell-diagram').ComponentType),
      buildPack: comp.language,
      services:
        comp.componentType === 'web-app'
          ? {
              [`${comp.name}:web`]: {
                id: `${comp.name}:web`,
                label: 'WebApp',
                type: 'HTTP',
                dependencyIds: (comp.dependsOn || []).map((depName) => `${depName}:api`),
                deploymentMetadata: {
                  gateways: {
                    internet: { isExposed: true },
                    intranet: { isExposed: false },
                  },
                },
              },
            }
          : comp.componentType === 'service'
            ? {
                [`${comp.name}:api`]: {
                  id: `${comp.name}:api`,
                  label: 'API',
                  type: 'HTTP',
                  dependencyIds: (comp.dependsOn || []).map((depName) => `${depName}:api`),
                  deploymentMetadata: {
                    gateways: {
                      internet: { isExposed: false },
                      intranet: { isExposed: false },
                    },
                  },
                },
              }
            : {},
      connections: (comp.dependsOn || []).map((depName) => ({
        id: `default:project:${depName}`,
        label: depName,
        onPlatform: true,
      })),
    })),
  };
}

// Stable signature of the diagram-relevant subset of state. The CellDiagram
// only depends on component names, types, languages, dependsOn, and the
// added/removed/preserved status overlay — not on overview text, requirements
// list, openAPISpec, or any other field that streams in. Keying memoization on
// this signature stops the diagram from re-rendering (and flickering) on every
// unrelated state change.
function buildDiagramSignature(
  components: DesignComponent[],
  statuses?: Record<string, ChangeType>,
): string {
  const visible = statuses
    ? components.filter((c) => statuses[c.name] !== 'removed')
    : components;
  return JSON.stringify(
    visible.map((c) => ({
      n: c.name,
      t: c.componentType,
      l: c.language,
      d: [...(c.dependsOn ?? [])].sort(),
      s: statuses ? (statuses[c.name] ?? '') : '',
    })),
  );
}

// Memoized wrapper around CellDiagram. React.memo's default reference equality
// is sufficient here because the parent passes a useMemo'd `project` keyed on
// `buildDiagramSignature`, so the prop reference only changes when the diagram
// content actually changes.
const MemoCellView = memo(function MemoCellView({
  project,
}: {
  project: import('@wso2/cell-diagram').Project;
}) {
  // eslint-disable-next-line no-console
  console.log('[diagram] CellDiagram rendered', {
    components: project.components.length,
    ids: project.components.map((c) => c.id),
  });
  return <CellView project={project} />;
});

// ---------------------------------------------------------------------------
// Display helpers
// ---------------------------------------------------------------------------

const typeLabels: Record<string, string> = {
  service: 'Service',
  'web-app': 'Web App',
  'scheduled-task': 'Cron Job',
};

const typeColors: Record<string, 'primary' | 'success' | 'warning' | 'info'> = {
  'web-app': 'primary',
  service: 'info',
  'scheduled-task': 'warning',
};

function displayName(kebab: string): string {
  return kebab
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ');
}

// ---------------------------------------------------------------------------
// Component card with collapsible OpenAPI spec
// ---------------------------------------------------------------------------

function ComponentCard({
  component,
  changeType,
  specUpdating,
}: {
  component: DesignComponent;
  changeType?: ChangeType;
  specUpdating?: boolean;
}) {
  const theme = useTheme();
  const [specExpanded, setSpecExpanded] = useState(false);

  // Diff-highlight styling — only rendered when an evolution is in progress
  // (regenerate flow). "preserved" renders like a normal card.
  const isAdded = changeType === 'added';
  const isRemoved = changeType === 'removed';

  const borderColor = isAdded
    ? 'success.main'
    : isRemoved
      ? 'error.main'
      : 'divider';

  return (
    <Card
      variant="outlined"
      sx={{
        height: '100%',
        borderRadius: 2,
        transition: 'box-shadow 0.2s',
        borderColor,
        borderWidth: isAdded || isRemoved ? 2 : 1,
        // Removed components are visually de-emphasised so the user reads them as "going away"
        opacity: isRemoved ? 0.72 : 1,
        bgcolor: isAdded
          ? 'rgba(76, 175, 80, 0.06)'
          : isRemoved
            ? 'rgba(244, 67, 54, 0.06)'
            : 'background.paper',
        '&:hover': { boxShadow: 2 },
      }}
    >
      <CardContent sx={{ p: 2.5, '&:last-child': { pb: 2.5 } }}>
        {/* Header */}
        <Stack direction="row" justifyContent="space-between" alignItems="flex-start" sx={{ mb: 2 }}>
          <Stack direction="row" alignItems="center" gap={1}>
            <Typography
              variant="subtitle1"
              fontWeight={700}
              sx={{
                textDecoration: isRemoved ? 'line-through' : undefined,
                color: isRemoved ? 'text.secondary' : undefined,
              }}
            >
              {displayName(component.name)}
            </Typography>
            {isAdded && (
              <Chip label="New" size="small" color="success" sx={{ height: 20, fontSize: '0.7rem' }} />
            )}
            {isRemoved && (
              <Chip label="Removed" size="small" color="error" sx={{ height: 20, fontSize: '0.7rem' }} />
            )}
          </Stack>
          <Chip
            label={typeLabels[component.componentType] || component.componentType}
            size="small"
            color={typeColors[component.componentType] ?? 'default'}
            variant="outlined"
          />
        </Stack>

        {/* Details */}
        <Stack spacing={1.5}>
          <Stack direction="row" justifyContent="space-between">
            <Typography variant="body2" color="text.secondary">
              Language
            </Typography>
            <Typography variant="body2" fontWeight={500}>
              {component.language}
            </Typography>
          </Stack>
          <Stack direction="row" justifyContent="space-between">
            <Typography variant="body2" color="text.secondary">
              Deployment
            </Typography>
            <Chip
              label={component.buildpack}
              size="small"
              variant="outlined"
              sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}
            />
          </Stack>

          {/* Dependencies */}
          {component.dependsOn && component.dependsOn.length > 0 && (
            <Stack direction="row" justifyContent="space-between" alignItems="flex-start">
              <Typography variant="body2" color="text.secondary">
                Depends on
              </Typography>
              <Stack
                direction="row"
                gap={0.5}
                flexWrap="wrap"
                justifyContent="flex-end"
                sx={{ maxWidth: '60%' }}
              >
                {component.dependsOn.map((depName) => (
                  <Chip
                    key={depName}
                    label={displayName(depName)}
                    size="small"
                    sx={{ fontSize: '0.7rem', height: 22 }}
                  />
                ))}
              </Stack>
            </Stack>
          )}
        </Stack>

        {/* OpenAPI Spec (collapsible) — also shows a spinner during streaming
            while the architect is calling set_openapi for this component. */}
        {(component.openAPISpec || specUpdating) && (
          <>
            <Divider sx={{ my: 2 }} />
            {specUpdating && !component.openAPISpec ? (
              <Stack
                direction="row"
                alignItems="center"
                gap={1}
                data-testid={`spec-updating-${component.name}`}
                sx={{ color: 'text.secondary', fontWeight: 500 }}
              >
                <CircularProgress size={14} thickness={5} />
                <Typography variant="body2" color="text.secondary">
                  Generating OpenAPI specification…
                </Typography>
              </Stack>
            ) : (
              <Button
                variant="text"
                size="small"
                onClick={() => setSpecExpanded(!specExpanded)}
                endIcon={
                  <ChevronRight
                    size={14}
                    style={{
                      transform: specExpanded ? 'rotate(90deg)' : 'rotate(0deg)',
                      transition: 'transform 0.2s',
                    }}
                  />
                }
                sx={{ textTransform: 'none', px: 0, color: 'text.secondary', fontWeight: 500 }}
              >
                OpenAPI Specification
              </Button>
            )}
            {specExpanded && component.openAPISpec && (
              <Box
                sx={{
                  mt: 1,
                  p: 2,
                  borderRadius: 1,
                  bgcolor: theme.vars?.palette.action?.hover ?? 'action.hover',
                  fontFamily: '"Fira Code", "Cascadia Code", "Consolas", monospace',
                  fontSize: '0.75rem',
                  lineHeight: 1.6,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                  maxHeight: 300,
                  overflowY: 'auto',
                }}
              >
                {component.openAPISpec}
              </Box>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export default function ProjectArchitecturePage() {
  const navigate = useNavigate();
  const location = useLocation();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';

  const navState = location.state as
    | { fromRequirements?: boolean; regenerate?: boolean }
    | null;
  const fromRequirements = navState?.fromRequirements === true;
  const regenerateIntent = navState?.regenerate === true;

  const generationAbort = useRef<AbortController | null>(null);

  const [design, setDesign] = useState<Design | null>(null);
  const [isGenerating, setIsGenerating] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isApproving, setIsApproving] = useState(false);
  const [approveError, setApproveError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [versions, setVersions] = useState<ArtifactVersion[]>([]);
  const [currentVersion, setCurrentVersion] = useState(0);
  const [viewingHistorical, setViewingHistorical] = useState(false);
  const [isDiscarding, setIsDiscarding] = useState(false);
  // Per-component diff status — populated during a v2+ regeneration so the
  // UI can highlight added/removed/preserved components. Empty for initial
  // generation and for plain loads/historical views.
  const [componentStatuses, setComponentStatuses] = useState<
    Record<string, ChangeType>
  >({});
  // Names of components for which the architect has called set_openapi but
  // the stream has not finalized yet. The corresponding ComponentCard shows
  // a "Generating OpenAPI specification…" spinner. Cleared on data-finish.
  const [specUpdatingNames, setSpecUpdatingNames] = useState<Set<string>>(
    () => new Set(),
  );

  const loadOrGenerate = useCallback(async () => {
    if (!projectId) return;

    if (fromRequirements) {
      // Clear navigation state to prevent re-generation on back/forward
      window.history.replaceState({}, '');

      const existing = await api.getDesign(routeOrgId, projectId);
      const hasExisting = !!existing && existing.status !== 'none';

      // --- V2+ regeneration path -------------------------------------------
      // Existing design is kept visible as the baseline; the stream layers
      // additions on top and marks removals, producing a diff view the user
      // can review before publishing.
      if (regenerateIntent && hasExisting && existing) {
        setIsLoading(false);
        setIsGenerating(true);
        // Clear the sourceSpec during streaming so the lineage label doesn't
        // display the stale upstream version until the new design finishes
        // and the refetch populates the new sourceSpec.
        setDesign({
          ...existing,
          sourceSpec: undefined,
          status: 'generating',
        });
        setVersions(existing.versions ?? []);
        setCurrentVersion(existing.version ?? 0);
        const baselineStatuses: Record<string, ChangeType> = {};
        for (const c of existing.components) baselineStatuses[c.name] = 'preserved';
        setComponentStatuses(baselineStatuses);

        let ok = false;
        try {
          const abort = new AbortController();
          generationAbort.current = abort;
          ok = await api.generateDesignStream(routeOrgId, projectId, {
            onOverview: (overview) =>
              setDesign((prev) => (prev ? { ...prev, overview } : prev)),
            onRequirements: (requirements) =>
              setDesign((prev) => (prev ? { ...prev, requirements } : prev)),
            onComponentAdded: (component) => {
              setDesign((prev) => {
                if (!prev) return prev;
                if (prev.components.some((c) => c.name === component.name)) return prev;
                return { ...prev, components: [...prev.components, component] };
              });
              setComponentStatuses((s) => ({ ...s, [component.name]: 'added' }));
            },
            onComponentUpdated: (name, patch) => {
              setDesign((prev) => {
                if (!prev) return prev;
                return {
                  ...prev,
                  components: prev.components.map((c) =>
                    c.name === name ? { ...c, ...patch } : c,
                  ),
                };
              });
            },
            onComponentRemoved: (name) => {
              setComponentStatuses((s) => ({ ...s, [name]: 'removed' }));
            },
            onComponentSpecUpdating: (name) => {
              setSpecUpdatingNames((prev) => {
                if (prev.has(name)) return prev;
                const next = new Set(prev);
                next.add(name);
                return next;
              });
            },
            onFinish: (finalDesign) => {
              // Replace components with final shape (including OpenAPI specs).
              setDesign((prev) =>
                prev
                  ? {
                      ...prev,
                      overview: finalDesign.overview,
                      requirements: finalDesign.requirements,
                      components: finalDesign.components,
                    }
                  : prev,
              );
              setSpecUpdatingNames(new Set());
            },
          }, abort.signal);
        } catch {
          // AbortError when user clicks refresh mid-generation — handleRefresh
          // already swapped in the re-fetched design.  Other errors surface
          // via the error state.
        } finally {
          generationAbort.current = null;
        }

        if (ok) {
          // Refetch metadata (hasUnsavedChanges, sourceSpec, versions) but
          // keep the streamed component list + diff statuses so the user
          // reviews the diff before publishing.
          const fresh = await api.getDesign(routeOrgId, projectId);
          if (fresh) {
            setDesign((prev) =>
              prev
                ? {
                    ...prev,
                    status: fresh.status,
                    hasUnsavedChanges: fresh.hasUnsavedChanges,
                    sourceSpec: fresh.sourceSpec,
                    version: fresh.version,
                    versions: fresh.versions,
                  }
                : fresh,
            );
            setVersions(fresh.versions ?? []);
            setCurrentVersion(fresh.version ?? 0);
          }
        } else {
          setError('Failed to regenerate architecture. Please try again.');
        }
        setIsGenerating(false);
        return;
      }

      // --- Existing design, no regen requested → just load it ---------------
      if (hasExisting && existing) {
        setDesign(existing);
        setVersions(existing.versions ?? []);
        setCurrentVersion(existing.version ?? 0);
        setIsLoading(false);
        return;
      }

      // --- First-time generation (v1) --------------------------------------
      setIsLoading(false);
      setIsGenerating(true);

      // Seed an empty draft so the page scaffold renders immediately and
      // components/overview can stream in.
      setDesign({
        projectId,
        overview: '',
        requirements: [],
        components: [],
        status: 'generating',
        version: 0,
      });

      let ok = false;
      try {
        const abort = new AbortController();
        generationAbort.current = abort;
        ok = await api.generateDesignStream(
          routeOrgId,
          projectId,
          {
            onOverview: (overview) =>
              setDesign((prev) => (prev ? { ...prev, overview } : prev)),
            onRequirements: (requirements) =>
              setDesign((prev) => (prev ? { ...prev, requirements } : prev)),
            onComponentAdded: (component) =>
              setDesign((prev) => {
                if (!prev) return prev;
                if (prev.components.some((c) => c.name === component.name)) return prev;
                return { ...prev, components: [...prev.components, component] };
              }),
            onComponentUpdated: (name, patch) =>
              setDesign((prev) => {
                if (!prev) return prev;
                return {
                  ...prev,
                  components: prev.components.map((c) =>
                    c.name === name ? { ...c, ...patch } : c,
                  ),
                };
              }),
            onComponentSpecUpdating: (name) =>
              setSpecUpdatingNames((prev) => {
                if (prev.has(name)) return prev;
                const next = new Set(prev);
                next.add(name);
                return next;
              }),
            onFinish: (finalDesign) => {
              setDesign((prev) =>
                prev
                  ? {
                      ...prev,
                      overview: finalDesign.overview,
                      requirements: finalDesign.requirements,
                      components: finalDesign.components,
                    }
                  : prev,
              );
              setSpecUpdatingNames(new Set());
            },
          },
          abort.signal,
        );
      } catch {
        // AbortError from refresh — handleRefresh already fetched latest.
      } finally {
        generationAbort.current = null;
      }

      if (ok) {
        // Re-fetch to pick up status, hasUnsavedChanges, versions, sourceSpec.
        const fresh = await api.getDesign(routeOrgId, projectId);
        if (fresh && fresh.status !== 'none') {
          setDesign(fresh);
          setVersions(fresh.versions ?? []);
          setCurrentVersion(fresh.version ?? 0);
        }
      } else {
        setError('Failed to generate architecture. Please try again.');
      }
      setIsGenerating(false);
    } else {
      // Direct navigation: load existing design
      const result = await api.getDesign(routeOrgId, projectId);
      if (result && result.status !== 'none') {
        setDesign(result);
        setVersions(result.versions ?? []);
        setCurrentVersion(result.version ?? 0);
      }
      setIsLoading(false);
    }
  }, [projectId, routeOrgId, fromRequirements, regenerateIntent]);

  useEffect(() => {
    loadOrGenerate();
  }, [loadOrGenerate]);

  const [isRefreshing, setIsRefreshing] = useState(false);

  // handleRefetch re-fetches the current design from the API without
  // re-triggering generation.  Used by the error banner's "Refresh" button
  // so the user can recover from a failed generation and see any existing
  // design (including one placed manually for troubleshooting).
  const handleRefetch = useCallback(async () => {
    if (!projectId) return;
    setIsRefreshing(true);
    setError(null);
    const result = await api.getDesign(routeOrgId, projectId);
    if (result && result.status !== 'none') {
      setDesign(result);
      setVersions(result.versions ?? []);
      setCurrentVersion(result.version ?? 0);
      setViewingHistorical(false);
      setComponentStatuses({});
    }
    setIsRefreshing(false);
  }, [projectId, routeOrgId]);

  // -- Diagram memoization ---------------------------------------------------
  // Build a stable signature of *only* the fields the CellDiagram cares about.
  // The full design object changes on every streaming chunk (overview text,
  // openAPISpec patches, etc.) so we cannot key memoization on it directly.
  // Hooks must run before the early returns below to keep render order stable.
  const diagramActiveStatuses = viewingHistorical ? undefined : componentStatuses;
  const diagramSignature = useMemo(
    () => buildDiagramSignature(design?.components ?? [], diagramActiveStatuses),
    [design?.components, diagramActiveStatuses],
  );
  const diagramProject = useMemo(
    () => buildProjectModel(design?.components ?? [], diagramActiveStatuses),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [diagramSignature],
  );

  // Debug instrumentation — prints on every parent render and notes when the
  // diagram-relevant signature changed since the previous render.
  const diagramRenderCount = useRef(0);
  const prevDiagramSignature = useRef<string | null>(null);
  diagramRenderCount.current += 1;
  const sigChanged = prevDiagramSignature.current !== diagramSignature;
  // eslint-disable-next-line no-console
  console.log('[diagram] page render', {
    n: diagramRenderCount.current,
    sigChanged,
    sigLen: diagramSignature.length,
    isGenerating,
    specUpdating: specUpdatingNames.size,
    components: design?.components?.length ?? 0,
  });
  prevDiagramSignature.current = diagramSignature;

  const handleVersionSelect = async (version: number) => {
    if (!projectId) return;
    // Leaving the live working copy drops the diff overlay.
    setComponentStatuses({});
    const latestVersion = versions.length > 0 ? Math.max(...versions.map((v) => v.version)) : 0;
    if (version === latestVersion) {
      const result = await api.getDesign(routeOrgId, projectId);
      if (result && result.status !== 'none') {
        setDesign(result);
        setCurrentVersion(result.version ?? 0);
      }
      setViewingHistorical(false);
    } else {
      const result = await api.getDesignAtVersion(routeOrgId, projectId, version);
      if (result) {
        setDesign(result);
        setCurrentVersion(version);
        setViewingHistorical(true);
      }
    }
  };

  const handleDiscard = async () => {
    if (!projectId) return;
    setIsDiscarding(true);
    const result = await api.discardDesignChanges(routeOrgId, projectId);
    if (result && result.status !== 'none') {
      setDesign(result);
      setCurrentVersion(result.version ?? 0);
      setVersions(result.versions ?? versions);
    }
    // Reverting to the last tag invalidates the in-flight diff.
    setComponentStatuses({});
    setIsDiscarding(false);
  };

  // -- Loading state -----------------------------------------------------------
  // todo: if (roomId && !connected)) , need to show a separate indicator
  if (isLoading) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <CircularProgress size={48} sx={{ mb: 3 }} />
          <Typography variant="h6" color="text.secondary">
            Loading architecture...
          </Typography>
        </Box>
      </PageContent>
    );
  }

  // -- Error state -------------------------------------------------------------

  // Error is shown as an inline banner within the main view, not as a
  // full-page replacement, so the user can see any existing design while
  // retrying failed generation.
  //   -- No design yet (and not streaming) ---------------------------------------

  if (!design) {
    if (isGenerating) {
      return (
        <PageContent>
          <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
            <CircularProgress size={48} sx={{ mb: 3 }} />
            <Typography variant="h6" color="text.secondary">
              Generating architecture...
            </Typography>
          </Box>
        </PageContent>
      );
    }
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <Typography variant="h6" color="text.secondary">
            No architecture generated yet.
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Go to the requirements page and click &quot;Generate Architecture&quot;.
          </Typography>
        </Box>
      </PageContent>
    );
  }

  if (!isGenerating && (!design.components || design.components.length === 0)) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <Typography variant="h6" color="text.secondary">
            No architecture generated yet.
          </Typography>
        </Box>
      </PageContent>
    );
  }

  // -- Main render -----------------------------------------------------------

  // Diff statuses only apply to the live working copy — not to a historical
  // version snapshot the user is browsing.
  const activeStatuses = viewingHistorical ? undefined : componentStatuses;
  const hasAnyStatus =
    !!activeStatuses && Object.keys(activeStatuses).length > 0;
  const addedCount = activeStatuses
    ? Object.values(activeStatuses).filter((s) => s === 'added').length
    : 0;
  const removedCount = activeStatuses
    ? Object.values(activeStatuses).filter((s) => s === 'removed').length
    : 0;
  const showDiff = hasAnyStatus && (addedCount > 0 || removedCount > 0);
  // The cards section header shows the target-state component count (exclude removed).
  const visibleComponentCount = activeStatuses
    ? design.components.filter((c) => activeStatuses[c.name] !== 'removed').length
    : design.components.length;

  return (
    <PageContent>
      {error && (
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2,
            py: 1,
            mb: 2,
            borderRadius: 1,
            bgcolor: 'error.light',
            color: 'error.contrastText',
          }}
        >
          <Typography variant="body2" sx={{ flex: 1 }}>
            {error}
          </Typography>
          <Button
            variant="text"
            size="small"
            onClick={handleRefetch}
            startIcon={isRefreshing ? <CircularProgress size={12} color="inherit" /> : <RefreshCcw size={12} />}
            disabled={isRefreshing}
            sx={{ minWidth: 0, color: 'inherit', opacity: 0.8, '&:hover': { opacity: 1 } }}
          >
            {isRefreshing ? 'Refreshing...' : 'Refresh'}
          </Button>
        </Box>
      )}
      {/* Overview Section */}
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 1 }}>
        <Stack direction="row" alignItems="center" gap={1.5}>
          <Typography variant="h4" fontWeight={700}>
            Architecture
          </Typography>
          <LineageLabel
            sourceSpec={
              viewingHistorical
                ? versions.find((v) => v.version === currentVersion)?.sourceSpec
                : design?.sourceSpec
            }
          />
          {isGenerating && (
            <Chip
              icon={<CircularProgress size={12} color="inherit" />}
              label="Generating..."
              size="small"
              color="info"
              variant="outlined"
            />
          )}
          {versions.length > 0 && (
            <VersionSelector
              versions={versions}
              currentVersion={currentVersion}
              onVersionSelect={handleVersionSelect}
              isHistorical={viewingHistorical}
              hasUnsavedChanges={design?.hasUnsavedChanges}
              onDiscard={handleDiscard}
              isDiscarding={isDiscarding}
            />
          )}
        </Stack>
        <Stack direction="row" alignItems="center" gap={1}>
          {!viewingHistorical && !isGenerating && (
            <Button
              variant="contained"
              size="small"
              startIcon={isApproving ? <CircularProgress size={14} color="inherit" /> : <Rocket size={16} />}
              disabled={isApproving}
              onClick={async () => {
                if (!projectId) return;
                setIsApproving(true);
                setApproveError(null);
                const result = await api.saveAndProceedDesign(routeOrgId, projectId);
                if (!result) {
                  setApproveError('Failed to save design. Please try again.');
                  setIsApproving(false);
                  return;
                }
                navigate(projectTasksPath(routeOrgId, projectId), {
                  state: { fromArchitecture: true },
                });
              }}
            >
              {isApproving ? 'Publishing...' : 'Publish'}
            </Button>
          )}
        </Stack>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        AI-generated architecture from your requirements.
      </Typography>
      {approveError && (
        <Typography variant="body2" color="error" sx={{ mb: 2 }}>
          {approveError}
        </Typography>
      )}

      {design.overview && (
        <Card variant="outlined" sx={{ mb: 4, borderRadius: 2 }}>
          <CardContent sx={{ p: 3, '&:last-child': { pb: 3 } }}>
            <Typography variant="h6" fontWeight={600} sx={{ mb: 1.5 }}>
              Overview
            </Typography>
            <Typography variant="body1" color="text.secondary" sx={{ lineHeight: 1.8 }}>
              {design.overview}
            </Typography>
          </CardContent>
        </Card>
      )}

      {/* Cell Diagram Section — always rendered; fills in as components stream in. */}
      <Typography variant="h6" fontWeight={600} sx={{ mb: 2 }}>
        Cell Diagram
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Visual representation of component boundaries and their interactions.
        {showDiff && (
          <>
            {' '}Removed components disappear from the diagram; new components appear as they are generated.
          </>
        )}
      </Typography>
      <Box
        sx={{
          mb: 4,
          height: 500,
          border: '1px solid',
          borderColor: 'divider',
          borderRadius: 2,
          overflow: 'hidden',
          position: 'relative',
        }}
      >
        <Suspense
          fallback={
            <Box
              sx={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                height: '100%',
              }}
            >
              <CircularProgress size={32} />
            </Box>
          }
        >
          <MemoCellView project={diagramProject} />
        </Suspense>
      </Box>

      {/* Components Section */}
      <Stack direction="row" alignItems="center" gap={1} sx={{ mb: 2 }} flexWrap="wrap">
        <Typography variant="h6" fontWeight={600}>
          Components
        </Typography>
        <Chip label={`${visibleComponentCount}`} size="small" variant="outlined" />
        {showDiff && addedCount > 0 && (
          <Chip
            label={`+${addedCount} added`}
            size="small"
            color="success"
            variant="outlined"
          />
        )}
        {showDiff && removedCount > 0 && (
          <Chip
            label={`-${removedCount} removed`}
            size="small"
            color="error"
            variant="outlined"
          />
        )}
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Each component will be scaffolded, implemented, and deployed independently.
      </Typography>

      <Grid container spacing={2.5} sx={{ mb: 4 }}>
        {design.components.map((comp) => (
          <Grid key={comp.name} size={{ xs: 12, md: 6 }}>
            <ComponentCard
              component={comp}
              changeType={activeStatuses ? activeStatuses[comp.name] : undefined}
              specUpdating={specUpdatingNames.has(comp.name)}
            />
          </Grid>
        ))}
      </Grid>
    </PageContent>
  );
}
