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
import { Package, Rocket, Sparkles } from '@wso2/oxygen-ui-icons-react';
import { Explorer, type CustomView, type ExplorerRef } from '@asdlc/explorer';
import { CELL_DIAGRAM_VIEW_ID, CellDiagramView } from '@asdlc/cell-diagram-view';
import { MdEditor } from '@asdlc/md-editor';
import { OpenApiView } from '@asdlc/openapi-view';
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

// Tree-display tweaks for the design Explorer. The on-disk layout is
// `.asdlc/design/components/<name>/{design.md,openapi.yaml}` but a "Components"
// folder row in the sidebar adds nothing the user cares about, so we collapse
// it. Each component then renders at top level with a package icon.
const ARCHITECTURE_TRANSPARENT_FOLDERS = new Set(['components']);
function getArchitectureFolderIcon(folderPath: string) {
  if (folderPath.startsWith('components/')) return <Package size={16} style={{ flexShrink: 0 }} />;
  return undefined;
}

// Route the per-component `openapi.yaml` files to the dedicated OpenAPI
// viewer. Every other path (component design.md, custom views, etc.)
// returns undefined so Explorer's default editor chain handles them.
const OPENAPI_PATH_RE = /^components\/[^/]+\/openapi\.ya?ml$/;
function renderOpenApiFile(path: string, content: string): React.ReactNode | undefined {
  if (!OPENAPI_PATH_RE.test(path)) return undefined;
  return <OpenApiView spec={content} />;
}
// User-facing label override: components/<x>/openapi.yaml reads as "API Spec"
// in the tree. The on-disk path stays unchanged.
function getArchitectureFileLabel(path: string): string | undefined {
  if (OPENAPI_PATH_RE.test(path)) return 'API Spec';
  return undefined;
}

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
      await api.saveAndProceedDesign(routeOrgId, projectId);
      clearAllDesignDrafts(routeOrgId, projectId);
      navigate(projectTasksPath(routeOrgId, projectId));
    } catch (err) {
      const msg = err instanceof Error && err.message ? err.message : 'Failed to publish design.';
      setPublishError(msg);
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

  const designMdContent = liveContents[DESIGN_ROOT_FILE] ?? '';
  const designReadOnly = viewingHistorical || generating;
  const handleDesignMdChange = useCallback(
    (md: string) => {
      handleFileChange(DESIGN_ROOT_FILE, md);
    },
    [handleFileChange],
  );

  // The "Component Design" view rolls the cell diagram and the top-level
  // architecture markdown into one page — the cell diagram on top, the
  // narrative on the bottom — so the user navigates once and sees the
  // whole system in context. The standalone `design.md` row is hidden
  // from the side tree below for the same reason.
  const customViews = useMemo<CustomView[]>(
    () => [
      {
        id: CELL_DIAGRAM_VIEW_ID,
        label: 'Component Design',
        // Single-scroll layout: the cell diagram is rendered as a fixed-height
        // "figure" at the top of the view, with the markdown editor flowing
        // beneath it in the same scroll context. The diagram reads as an
        // embedded picture — non-interactive in the markdown sense (you can't
        // edit it by typing), and it scrolls out of the way as the user reads
        // or edits the narrative below.
        content: (
          <Box
            sx={{
              height: '100%',
              overflowY: 'auto',
              display: 'flex',
              flexDirection: 'column',
              bgcolor: 'background.paper',
            }}
          >
            {/* The diagram is sized to match the markdown content column
                (816px, centered) with a soft framed look so it reads as a
                figure inset inside the document — not as a top panel that
                happens to live above another panel. */}
            <Box
              aria-label="Architecture cell diagram"
              sx={{
                flexShrink: 0,
                pt: 4,
                pb: 2,
                px: 3,
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
              }}
            >
              <Box sx={{ width: '100%', maxWidth: 816 }}>
                <Typography
                  variant="overline"
                  component="h3"
                  sx={{
                    m: 0,
                    mb: 1,
                    color: 'text.secondary',
                    letterSpacing: '0.08em',
                    fontSize: 11,
                    fontWeight: 600,
                  }}
                >
                  Cell Diagram
                </Typography>
                <Box
                  sx={{
                    width: '100%',
                    height: 640,
                    display: 'flex',
                    borderRadius: 1.5,
                    border: 1,
                    borderColor: 'divider',
                    overflow: 'hidden',
                    position: 'relative',
                    // Clip the diagram's portal-mounted zoom controls so the
                    // figure reads as a picture, not a canvas surface.
                    '& button[aria-label^="Zoom"]': { display: 'none' },
                  }}
                >
                  <CellDiagramView components={effectiveComponents} />
                </Box>
              </Box>
            </Box>
            <Box sx={{ flexShrink: 0 }}>
              {/* Toolbar suppressed — the editor sits inside the same
                  document surface as the diagram above; a toolbar bar
                  would reintroduce the panel-on-panel feel. Inline
                  markdown still works (`**bold**`, `# heading`, …). */}
              <MdEditor
                value={designMdContent}
                onChange={handleDesignMdChange}
                readOnly={designReadOnly}
                showToolbar={false}
                placeholder="System architecture overview…"
              />
            </Box>
          </Box>
        ),
      },
    ],
    [
      effectiveComponents,
      designMdContent,
      handleDesignMdChange,
      designReadOnly,
    ],
  );

  // Strip the root `design.md` from the file list passed to Explorer — its
  // content already lives in the merged "Component Design" view above.
  // Doing it here (instead of skipping it in `setLiveContents`) keeps
  // auto-save and version history working through the existing buffer.
  const treeFiles = useMemo(() => {
    if (liveContents[DESIGN_ROOT_FILE] === undefined) return liveContents;
    const { [DESIGN_ROOT_FILE]: _hidden, ...rest } = liveContents;
    return rest;
  }, [liveContents]);

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
            files={treeFiles}
            customViews={customViews}
            pendingPaths={pendingArtifacts}
            // The `components/` directory is an organisational detail on disk;
            // in the tree we want each component to read as a top-level
            // entity, so hide the parent folder and promote its children up.
            transparentFolders={ARCHITECTURE_TRANSPARENT_FOLDERS}
            // Component folders (children of `components/`) display as the
            // unit that gets built and shipped, so give them a package icon.
            getFolderIcon={getArchitectureFolderIcon}
            // Heading-level outlines under each file in the side tree add
            // noise on this page — the cell diagram is the primary navigator.
            showHeadings={false}
            // Render `components/<x>/openapi.yaml` as a swagger-style docs
            // page instead of falling through to the markdown editor with
            // raw YAML. All other paths use the default editor chain.
            getFileRenderer={renderOpenApiFile}
            // …and display it in the tree as "API Spec" — the filename is
            // an implementation detail.
            getFileLabel={getArchitectureFileLabel}
            activePath={activePath}
            onActivePathChange={setActivePath}
            onFileChange={handleFileChange}
            // Manual file-add is intentionally omitted on the architecture
            // page — components live and die with the design regeneration
            // flow, not with hand-edited paths in the tree.
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
