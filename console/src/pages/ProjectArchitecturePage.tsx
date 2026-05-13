import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useOutletContext, useParams } from 'react-router-dom';
import {
  Box,
  Button,
  CircularProgress,
  Divider,
  PageContent,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { Rocket, Sparkles } from '@wso2/oxygen-ui-icons-react';
import { Explorer, type AddFileMenuItem, type CustomView, type ExplorerRef } from '@asdlc/explorer';
import { CELL_DIAGRAM_VIEW_ID, createCellDiagramView } from '@asdlc/cell-diagram-view';
import { api } from '../services/api';
import type { ArtifactVersion, Design, DesignComponent } from '../services/api';
import { projectTasksPath } from '../lib/paths';
import VersionSelector from '../components/VersionSelector';
import LineageLabel from '../components/LineageLabel';
import {
  DESIGN_DOCUMENT_TYPES,
  componentDesignPath,
  componentNameFromPath,
  componentOpenApiPath,
  defaultComponentDesignMd,
  defaultComponentOpenApi,
  designDocumentTypeForPath,
} from '../lib/designDocumentTypes';
import {
  clearAllDesignDrafts,
  loadDesignDrafts,
  saveDesignDraft,
} from '../lib/designDraftStorage';

interface LayoutContext {
  setSidebarCollapsed: (collapsed: boolean) => void;
}

const AUTO_SAVE_DEBOUNCE_MS = 1500;
const DESIGN_ROOT_FILE = 'design.md';

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function ProjectArchitecturePage() {
  const { setSidebarCollapsed } = useOutletContext<LayoutContext>();
  useEffect(() => setSidebarCollapsed(true), [setSidebarCollapsed]);

  const navigate = useNavigate();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';

  const [loading, setLoading] = useState(true);
  const [savedFiles, setSavedFiles] = useState<Record<string, string>>({});
  const [liveContents, setLiveContents] = useState<Record<string, string>>({});
  const [activePath, setActivePath] = useState<string | null>(CELL_DIAGRAM_VIEW_ID);
  const [design, setDesign] = useState<Design | null>(null);
  const [versions, setVersions] = useState<ArtifactVersion[]>([]);
  const [currentVersion, setCurrentVersion] = useState(0);
  const [viewingHistorical, setViewingHistorical] = useState(false);
  const [hasUnsavedChanges, setHasUnsavedChanges] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [publishError, setPublishError] = useState<string | null>(null);

  // Streaming state populated from architect SSE events while `generating`.
  // The cell diagram reads from `streamingComponents` and the file tree shows
  // a spinner on every path in `pendingArtifacts`. Both reset on finish.
  const [streamingComponents, setStreamingComponents] = useState<DesignComponent[]>([]);
  const [pendingArtifacts, setPendingArtifacts] = useState<Set<string>>(() => new Set());

  const editorRef = useRef<ExplorerRef>(null);
  const autoSaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Load bundle.
  const refreshBundle = useCallback(async () => {
    if (!projectId) return;
    const bundle = await api.getDesignBundle(routeOrgId, projectId);
    if (!bundle) {
      setSavedFiles({});
      setDesign(null);
      return;
    }
    setSavedFiles(bundle.files);
    setDesign(bundle.design);
    setLiveContents(bundle.files);
    setHasUnsavedChanges(bundle.design?.hasUnsavedChanges ?? false);
    setCurrentVersion(bundle.design?.version ?? 0);
    if (bundle.design?.versions) setVersions(bundle.design.versions);
    if (activePath === null && bundle.files[DESIGN_ROOT_FILE]) {
      setActivePath(DESIGN_ROOT_FILE);
    }
  }, [routeOrgId, projectId, activePath]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      await refreshBundle();
      if (!cancelled) setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeOrgId, projectId]);

  // Restore drafts from localStorage on first load.
  useEffect(() => {
    if (!projectId) return;
    const drafts = loadDesignDrafts(routeOrgId, projectId);
    if (Object.keys(drafts).length === 0) return;
    setLiveContents((prev) => {
      const next = { ...prev };
      for (const [path, draft] of Object.entries(drafts)) {
        if (next[path] !== draft.draft) next[path] = draft.draft;
      }
      return next;
    });
  }, [routeOrgId, projectId]);

  // Auto-save on edit.
  const scheduleAutoSave = useCallback(
    (path: string, content: string) => {
      if (viewingHistorical) return;
      if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current);
      autoSaveTimer.current = setTimeout(async () => {
        if (!projectId) return;
        const bundle = await api.updateDesignFile(routeOrgId, projectId, path, content);
        if (bundle) {
          setSavedFiles(bundle.files);
          setDesign(bundle.design);
          setHasUnsavedChanges(bundle.design?.hasUnsavedChanges ?? false);
          if (bundle.design?.versions) setVersions(bundle.design.versions);
        }
      }, AUTO_SAVE_DEBOUNCE_MS);
    },
    [routeOrgId, projectId, viewingHistorical],
  );

  const handleFileChange = useCallback(
    (path: string, md: string) => {
      setLiveContents((prev) => ({ ...prev, [path]: md }));
      if (projectId) {
        saveDesignDraft(routeOrgId, projectId, path, {
          draft: md,
          baseServerContent: savedFiles[path] ?? '',
          savedAt: Date.now(),
        });
      }
      scheduleAutoSave(path, md);
    },
    [routeOrgId, projectId, savedFiles, scheduleAutoSave],
  );

  // Add file menu — Add component / Add OpenAPI for current component.
  const addFileMenu = useMemo<{ items: AddFileMenuItem[] }>(() => {
    const items: AddFileMenuItem[] = [
      {
        id: 'component',
        label: 'Add component',
        description: 'Create a new components/<name>/ directory with design.md.',
      },
    ];
    const activeComp = activePath ? componentNameFromPath(activePath) : undefined;
    if (activeComp && !savedFiles[componentOpenApiPath(activeComp)]) {
      items.push({
        id: 'openapi',
        label: `Add OpenAPI for ${activeComp}`,
        description: 'Create components/<name>/openapi.yaml.',
      });
    }
    return { items };
  }, [activePath, savedFiles]);

  const handleAddFile = useCallback(
    (typeId?: string): string | undefined | void => {
      if (!projectId) return undefined;
      if (typeId === 'component') {
        const name = window.prompt('New component name (lowercase, kebab-case):', '');
        if (!name) return undefined;
        const slug = name.trim().toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^-+|-+$/g, '');
        if (!slug) return undefined;
        const path = componentDesignPath(slug);
        const content = defaultComponentDesignMd({ name: slug, type: 'service', language: 'Go' });
        void (async () => {
          const bundle = await api.updateDesignFile(routeOrgId, projectId, path, content);
          if (bundle) {
            setSavedFiles(bundle.files);
            setDesign(bundle.design);
            setLiveContents(bundle.files);
            setActivePath(path);
          }
        })();
        return path;
      }
      if (typeId === 'openapi') {
        const activeComp = activePath ? componentNameFromPath(activePath) : undefined;
        if (!activeComp) return undefined;
        const path = componentOpenApiPath(activeComp);
        void (async () => {
          const bundle = await api.updateDesignFile(
            routeOrgId,
            projectId,
            path,
            defaultComponentOpenApi(activeComp),
          );
          if (bundle) {
            setSavedFiles(bundle.files);
            setDesign(bundle.design);
            setLiveContents(bundle.files);
            setActivePath(path);
          }
        })();
        return path;
      }
      return undefined;
    },
    [routeOrgId, projectId, activePath],
  );

  const handleDelete = useCallback(
    async (path: string) => {
      if (!projectId) return;
      // Refuse root design.md.
      const docType = designDocumentTypeForPath(path);
      if (docType?.protected) return;
      // Component design.md → delete the whole components/<name>/ dir for cleanliness.
      const compName = componentNameFromPath(path);
      const isComponentRoot = compName && path === componentDesignPath(compName);
      const bundle = isComponentRoot && compName
        ? await api.deleteDesignComponent(routeOrgId, projectId, compName)
        : await api.deleteDesignFile(routeOrgId, projectId, path);
      if (bundle) {
        setSavedFiles(bundle.files);
        setDesign(bundle.design);
        setLiveContents(bundle.files);
        if (activePath === path) setActivePath(DESIGN_ROOT_FILE);
      }
    },
    [routeOrgId, projectId, activePath],
  );

  const handleVersionSelect = useCallback(
    async (tag: string | null) => {
      if (!projectId) return;
      if (!tag) {
        setViewingHistorical(false);
        await refreshBundle();
        return;
      }
      const bundle = await api.getDesignBundleAtVersion(routeOrgId, projectId, tag);
      if (bundle) {
        setSavedFiles(bundle.files);
        setLiveContents(bundle.files);
        setDesign(bundle.design);
        setViewingHistorical(true);
        if (activePath !== CELL_DIAGRAM_VIEW_ID && !bundle.files[activePath ?? '']) {
          setActivePath(DESIGN_ROOT_FILE);
        }
      }
    },
    [routeOrgId, projectId, activePath, refreshBundle],
  );

  const handleGenerate = useCallback(async () => {
    if (!projectId || generating) return;
    setGenerating(true);
    setPublishError(null);

    // Reset streaming state. The root design.md goes pending immediately
    // (it's recomposed from overview + requirements when finalize runs).
    // Components and their files are added as `component-added` events
    // arrive. Pull the user onto the cell diagram so they see it populate
    // as each component lands.
    setStreamingComponents([]);
    setPendingArtifacts(new Set([DESIGN_ROOT_FILE]));
    setLiveContents((prev) => {
      // Seed an empty design.md so the row appears in the tree with its
      // spinner even on a from-scratch generation (no saved files yet).
      if (prev[DESIGN_ROOT_FILE] !== undefined) return prev;
      return { ...prev, [DESIGN_ROOT_FILE]: '' };
    });
    setActivePath(CELL_DIAGRAM_VIEW_ID);

    try {
      await api.generateDesignStream(routeOrgId, projectId, {
        onComponentAdded: (component) => {
          setStreamingComponents((prev) =>
            prev.some((c) => c.name === component.name) ? prev : [...prev, component],
          );
          const designPath = componentDesignPath(component.name);
          const openapiPath = componentOpenApiPath(component.name);
          setPendingArtifacts((prev) => {
            const next = new Set(prev);
            next.add(designPath);
            // Only services get an openapi.yaml — match BFF's writer.
            if (component.componentType === 'service') next.add(openapiPath);
            return next;
          });
          // Seed placeholders so the file rows appear in the tree under a
          // spinner; the real content arrives in the post-finish bundle.
          setLiveContents((prev) => {
            const next = { ...prev };
            if (next[designPath] === undefined) next[designPath] = '';
            if (component.componentType === 'service' && next[openapiPath] === undefined) {
              next[openapiPath] = '';
            }
            return next;
          });
        },
        onComponentUpdated: (name, patch) => {
          setStreamingComponents((prev) =>
            prev.map((c) => (c.name === name ? { ...c, ...patch } : c)),
          );
        },
        onComponentRemoved: (name) => {
          setStreamingComponents((prev) => prev.filter((c) => c.name !== name));
          const designPath = componentDesignPath(name);
          const openapiPath = componentOpenApiPath(name);
          setPendingArtifacts((prev) => {
            const next = new Set(prev);
            next.delete(designPath);
            next.delete(openapiPath);
            return next;
          });
          setLiveContents((prev) => {
            const next = { ...prev };
            delete next[designPath];
            delete next[openapiPath];
            return next;
          });
        },
      });
      await refreshBundle();
    } finally {
      setGenerating(false);
      setStreamingComponents([]);
      setPendingArtifacts(new Set());
    }
  }, [routeOrgId, projectId, generating, refreshBundle]);

  const handlePublish = useCallback(async () => {
    if (!projectId || publishing) return;
    setPublishing(true);
    setPublishError(null);
    try {
      const result = await api.saveAndProceedDesign(routeOrgId, projectId);
      if (!result) {
        setPublishError('Failed to publish design.');
        return;
      }
      clearAllDesignDrafts(routeOrgId, projectId);
      navigate(projectTasksPath(routeOrgId, projectId));
    } finally {
      setPublishing(false);
    }
  }, [routeOrgId, projectId, publishing, navigate]);

  const handleDiscard = useCallback(async () => {
    if (!projectId) return;
    await api.discardDesignChanges(routeOrgId, projectId);
    if (projectId) clearAllDesignDrafts(routeOrgId, projectId);
    await refreshBundle();
  }, [routeOrgId, projectId, refreshBundle]);

  // While generating, render the cell diagram from streaming components so
  // each `component-added` event progressively populates the canvas. Once
  // finalize fires and the bundle refreshes, fall back to design.components.
  const effectiveComponents = generating ? streamingComponents : (design?.components ?? []);
  const customViews = useMemo<CustomView[]>(
    () => [createCellDiagramView({ components: effectiveComponents })],
    [effectiveComponents],
  );

  if (loading) {
    return (
      <PageContent>
        <Stack alignItems="center" sx={{ py: 8 }}>
          <CircularProgress />
        </Stack>
      </PageContent>
    );
  }

  return (
    <PageContent fullWidth noPadding sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Box
        sx={{
          px: 3,
          py: 1.5,
          borderBottom: 1,
          borderColor: 'divider',
          display: 'flex',
          alignItems: 'center',
          gap: 2,
          bgcolor: 'background.paper',
          flexShrink: 0,
        }}
      >
        <Box sx={{ flexGrow: 1, minWidth: 0 }}>
          <Stack direction="row" alignItems="center" gap={1.5}>
            <Typography variant="h4">Architecture</Typography>
            <LineageLabel sourceSpec={design?.sourceSpec} />
            {versions.length > 0 && (
              <VersionSelector
                versions={versions}
                currentVersion={currentVersion}
                isHistorical={viewingHistorical}
                onVersionSelect={(v) => {
                  const tag = versions.find((x) => x.version === v)?.tagName ?? null;
                  void handleVersionSelect(tag);
                }}
                hasUnsavedChanges={hasUnsavedChanges}
                onDiscard={handleDiscard}
              />
            )}
          </Stack>
        </Box>

        <Button
          variant="outlined"
          size="small"
          startIcon={generating ? <CircularProgress size={14} /> : <Sparkles size={16} />}
          onClick={handleGenerate}
          disabled={generating || viewingHistorical}
        >
          {generating ? 'Generating…' : design ? 'Regenerate' : 'Generate'}
        </Button>

        {!viewingHistorical && (
          <>
            <Divider orientation="vertical" flexItem />
            <Button
              variant="contained"
              size="small"
              startIcon={publishing ? <CircularProgress size={14} color="inherit" /> : <Rocket size={16} />}
              onClick={handlePublish}
              disabled={!hasUnsavedChanges || publishing || !design}
            >
              {publishing ? 'Publishing…' : 'Publish'}
            </Button>
          </>
        )}
      </Box>

      {publishError && (
        <Box sx={{ px: 3, py: 1, borderBottom: 1, borderColor: 'divider', flexShrink: 0 }}>
          <Typography variant="body2" color="error">
            {publishError}
          </Typography>
        </Box>
      )}

      <Box sx={{ flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <Box sx={{ flex: 1, minHeight: 0, display: 'flex' }}>
          <Explorer
            files={liveContents}
            customViews={customViews}
            pendingPaths={pendingArtifacts}
            activePath={activePath}
            onActivePathChange={setActivePath}
            onFileChange={handleFileChange}
            onAddFile={handleAddFile}
            addFileMenu={addFileMenu}
            onDelete={handleDelete}
            editorRef={editorRef}
            searchPlaceholder="Search files"
            editorProps={{
              readOnly: viewingHistorical || generating,
              placeholder: 'Edit the design…',
            }}
          />
        </Box>
      </Box>

      {DESIGN_DOCUMENT_TYPES.length > 0 && null /* keep import used for tree-shake stability */}
    </PageContent>
  );
}
