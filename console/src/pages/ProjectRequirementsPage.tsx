import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useOutletContext, useParams } from 'react-router-dom';
import {
  Box,
  Button,
  CircularProgress,
  Divider,
  PageContent,
  Stack,
  Tooltip,
  Typography,
} from '@wso2/oxygen-ui';
import { GitCompare, GitHub, Rocket, Sparkles } from '@wso2/oxygen-ui-icons-react';
import type { CollabConfig } from '@asdlc/md-editor';
import { Explorer, type ExplorerRef, type AddFileMenuItem } from '@asdlc/explorer';
import { api, ApiError } from '../services/api';
import type { ArtifactVersion } from '../services/api';
import { projectArchitecturePath } from '../lib/paths';
import { useCollabEditor } from '../hooks/useCollabEditor';
import CollabAwarenessBar from '../components/CollabAwarenessBar';
import VersionSelector from '../components/VersionSelector';
import {
  clearAllDrafts,
  clearDraft,
  loadDrafts,
  saveDraft,
} from '../lib/requirementsDraftStorage';
import {
  DOCUMENT_TYPES,
  documentTypeForFile,
  getDocumentType,
  nextFilenameFor,
  type DocumentType,
} from '../lib/documentTypes';

interface LayoutContext {
  setSidebarCollapsed: (collapsed: boolean) => void;
}

type SaveStatus = 'idle' | 'unsaved' | 'saving' | 'saved' | 'error';

const AUTO_SAVE_DEBOUNCE_MS = 1500;
const REQUIREMENTS_MAIN_FILE = 'requirements.md';

function formatRelative(ts: number): string {
  const diff = Math.max(0, Date.now() - ts);
  const min = Math.round(diff / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min} minute${min === 1 ? '' : 's'} ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr} hour${hr === 1 ? '' : 's'} ago`;
  const day = Math.round(hr / 24);
  return `${day} day${day === 1 ? '' : 's'} ago`;
}

export default function ProjectRequirementsPage() {
  const { setSidebarCollapsed } = useOutletContext<LayoutContext>();
  useEffect(() => {
    setSidebarCollapsed(true);
  }, [setSidebarCollapsed]);

  const navigate = useNavigate();
  const location = useLocation();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';

  const streamPrompt = (location.state as { streamPrompt?: string } | null)?.streamPrompt ?? null;

  const [loading, setLoading] = useState(!streamPrompt);
  // Saved (server-known) file map. liveContents tracks editor buffers.
  const [savedFiles, setSavedFiles] = useState<Record<string, string>>({});
  const [liveContents, setLiveContents] = useState<Record<string, string>>({});
  const [activePath, setActivePath] = useState<string | null>(null);

  const [roomId, setRoomId] = useState<string | null>(null);
  const [generatingFile, setGeneratingFile] = useState<string | null>(null);
  const [publishError, setPublishError] = useState<string | null>(null);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [streamingMain, setStreamingMain] = useState(!!streamPrompt);
  const [versions, setVersions] = useState<ArtifactVersion[]>([]);
  const [currentVersion, setCurrentVersion] = useState(0);
  const [viewingHistorical, setViewingHistorical] = useState(false);
  const [historicalFiles, setHistoricalFiles] = useState<Record<string, string> | null>(null);
  const [hasUnsavedChanges, setHasUnsavedChanges] = useState(false);
  const [isDiscarding, setIsDiscarding] = useState(false);
  const [lastTaggedActive, setLastTaggedActive] = useState<string | null>(null);
  const [repoUrl, setRepoUrl] = useState<string>('');
  const [userName, setUserName] = useState<string | undefined>(undefined);
  const [publishing, setPublishing] = useState(false);

  const [saveStatus, setSaveStatus] = useState<SaveStatus>('idle');
  const [showDiff, setShowDiff] = useState(false);
  const [restorePromptFor, setRestorePromptFor] = useState<{
    filename: string;
    localDraft: string;
    serverContent: string;
    savedAt: number;
  } | null>(null);

  const editorRef = useRef<ExplorerRef>(null);
  const userTypedRef = useRef(false);
  const restoreCheckedRef = useRef(false);
  const startedRef = useRef(false);
  const abortRef = useRef<AbortController | null>(null);

  // -- Collab (scoped to the active file's content) -------------------------

  const handleCollabSave = useCallback(
    async (val: string) => {
      if (!projectId || !activePath) return;
      const updated = await api.updateRequirementFile(routeOrgId, projectId, activePath, val);
      if (updated?.files) {
        setSavedFiles(updated.files);
        setHasUnsavedChanges(updated.hasUnsavedChanges ?? false);
        if (updated.versions) setVersions(updated.versions);
        setCurrentVersion(updated.version ?? 0);
      }
    },
    [routeOrgId, projectId, activePath],
  );

  const handleSeedRequested = useCallback((markdown: string) => {
    editorRef.current?.setActiveMarkdown(markdown);
  }, []);

  const { connected, peers, ydoc, provider, user } = useCollabEditor({
    roomId,
    orgId: routeOrgId,
    projectId,
    getMarkdown: () => editorRef.current?.getActiveMarkdown() ?? '',
    onSave: handleCollabSave,
    onSeedRequested: handleSeedRequested,
    isEditing: true,
    userName,
  });

  const collabConfig: CollabConfig | undefined = useMemo(() => {
    if (!ydoc || !provider || !user) return undefined;
    return { ydoc, provider, user };
  }, [ydoc, provider, user]);

  // -- Initial load + streaming bootstrap ----------------------------------

  useEffect(() => {
    if (!projectId) return;

    if (streamPrompt && !startedRef.current) {
      startedRef.current = true;
      navigate(location.pathname, { replace: true });
      setStreamError(null);

      const controller = new AbortController();
      abortRef.current = controller;
      // Streaming bootstrap creates `requirements.md` if missing and writes
      // the streamed content into it.
      let accumulated = '';
      api
        .generateRequirementFile(
          routeOrgId,
          projectId,
          REQUIREMENTS_MAIN_FILE,
          { skillId: 'requirements-from-prompt', prompt: streamPrompt },
          (delta) => {
            if (!delta) return;
            accumulated += delta;
            setLiveContents((prev) => ({ ...prev, [REQUIREMENTS_MAIN_FILE]: accumulated }));
            setSavedFiles((prev) => ({ ...prev, [REQUIREMENTS_MAIN_FILE]: accumulated }));
            setActivePath(REQUIREMENTS_MAIN_FILE);
          },
          controller.signal,
        )
        .then(async (ok) => {
          if (controller.signal.aborted) return;
          setStreamingMain(false);
          if (ok) {
            await refreshAll();
          } else {
            setStreamError('Failed to generate requirements. Please try again.');
          }
        })
        .catch(() => {
          if (controller.signal.aborted) return;
          setStreamingMain(false);
          setStreamError('Failed to generate requirements. Please try again.');
        });
      return;
    }

    (async () => {
      await refreshAll();
      setLoading(false);
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [streamPrompt, routeOrgId, projectId, navigate, location.pathname]);

  useEffect(() => () => abortRef.current?.abort(), []);

  // Reset per-project refs when the route changes.
  useEffect(() => {
    restoreCheckedRef.current = false;
    userTypedRef.current = false;
    setSaveStatus('idle');
    setShowDiff(false);
    setRestorePromptFor(null);
    setActivePath(null);
  }, [routeOrgId, projectId]);

  const refreshAll = useCallback(async () => {
    if (!projectId) return;
    const [bundle, session] = await Promise.all([
      api.getRequirements(routeOrgId, projectId),
      api.getCollabSession(routeOrgId, projectId),
    ]);
    if (bundle) {
      setSavedFiles(bundle.files ?? {});
      setLiveContents(bundle.files ?? {});
      setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
      setCurrentVersion(bundle.version ?? 0);
      if (bundle.versions) setVersions(bundle.versions);
      // Activate `requirements.md` by default; fall back to first file.
      setActivePath((prev) => {
        if (prev && (bundle.files ?? {})[prev] !== undefined) return prev;
        if ((bundle.files ?? {})[REQUIREMENTS_MAIN_FILE] !== undefined) return REQUIREMENTS_MAIN_FILE;
        const keys = Object.keys(bundle.files ?? {});
        return keys[0] ?? null;
      });
    }
    if (session?.roomId) setRoomId(session.roomId);
    if (session) setUserName(session.userName || session.email || 'Anonymous');
  }, [routeOrgId, projectId]);

  // Repo URL banner.
  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    (async () => {
      const status = await api.getProjectStatus(routeOrgId, projectId);
      if (!cancelled && status?.repoUrl) {
        setRepoUrl(status.repoUrl);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [routeOrgId, projectId]);

  // Fetch the active file's content at the latest tag for diffing.
  useEffect(() => {
    if (!projectId || !activePath || !hasUnsavedChanges || versions.length === 0) {
      setLastTaggedActive(null);
      return;
    }
    const latest = Math.max(...versions.map((v) => v.version));
    const latestTag = `v${latest}`;
    let cancelled = false;
    (async () => {
      const at = await api.getRequirementsAtVersion(routeOrgId, projectId, latestTag);
      if (!cancelled) setLastTaggedActive(at?.files?.[activePath] ?? null);
    })();
    return () => {
      cancelled = true;
    };
  }, [routeOrgId, projectId, activePath, hasUnsavedChanges, versions]);

  // Restore-on-mount draft check (per-file).
  useEffect(() => {
    if (restoreCheckedRef.current) return;
    if (!projectId || loading || streamingMain) return;
    restoreCheckedRef.current = true;

    const drafts = loadDrafts(routeOrgId, projectId);
    const filenames = Object.keys(drafts);
    for (const filename of filenames) {
      const local = drafts[filename]!;
      const serverContent = savedFiles[filename] ?? '';
      if (local.draft === serverContent) {
        clearDraft(routeOrgId, projectId, filename);
        continue;
      }
      if (local.baseServerContent !== serverContent) {
        // Pop the prompt for the first divergent draft we find. Multi-file
        // conflict resolution is left intentionally simple: one file at a
        // time.
        setRestorePromptFor({
          filename,
          localDraft: local.draft,
          serverContent,
          savedAt: local.savedAt,
        });
        break;
      }
      // Server still at the snapshot we last saw — restore silently and replay PUT.
      userTypedRef.current = true;
      setLiveContents((prev) => ({ ...prev, [filename]: local.draft }));
      if (filename === activePath) editorRef.current?.setActiveMarkdown(local.draft);
      setSaveStatus('saving');
      api
        .updateRequirementFile(routeOrgId, projectId, filename, local.draft)
        .then((bundle) => {
          if (bundle?.files) {
            setSavedFiles(bundle.files);
            setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
            if (bundle.versions) setVersions(bundle.versions);
          }
          saveDraft(routeOrgId, projectId, filename, {
            draft: local.draft,
            baseServerContent: local.draft,
            savedAt: Date.now(),
          });
          setSaveStatus('saved');
        })
        .catch(() => setSaveStatus('error'));
    }
  }, [routeOrgId, projectId, loading, streamingMain, savedFiles, activePath]);

  // Auto-save: debounced PUT for the active file when its buffer differs from saved.
  useEffect(() => {
    if (!projectId || !activePath) return;
    if (streamingMain || viewingHistorical) return;
    if (!userTypedRef.current) return;
    const buf = liveContents[activePath] ?? '';
    const saved = savedFiles[activePath] ?? '';

    saveDraft(routeOrgId, projectId, activePath, {
      draft: buf,
      baseServerContent: saved,
      savedAt: Date.now(),
    });

    if (buf === saved) return;
    if (connected) return; // collab loop owns the save when connected

    setSaveStatus('unsaved');
    const t = setTimeout(async () => {
      setSaveStatus('saving');
      try {
        await api.updateRequirementFile(routeOrgId, projectId, activePath, buf);
        setSavedFiles((prev) => ({ ...prev, [activePath]: buf }));
        const bundle = await api.getRequirements(routeOrgId, projectId);
        if (bundle) {
          setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
          if (bundle.versions) setVersions(bundle.versions);
          setCurrentVersion(bundle.version ?? 0);
        }
        saveDraft(routeOrgId, projectId, activePath, {
          draft: buf,
          baseServerContent: buf,
          savedAt: Date.now(),
        });
        setSaveStatus('saved');
      } catch {
        setSaveStatus('error');
      }
    }, AUTO_SAVE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [
    liveContents,
    savedFiles,
    activePath,
    streamingMain,
    viewingHistorical,
    connected,
    routeOrgId,
    projectId,
  ]);

  // beforeunload — native dialog when leaving with unsaved/saving work.
  useEffect(() => {
    const dirty = saveStatus === 'unsaved' || saveStatus === 'saving';
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [saveStatus]);

  // -- Version + discard ---------------------------------------------------

  const handleVersionSelect = async (version: number) => {
    if (!projectId) return;
    const latestVersion = versions.length > 0 ? Math.max(...versions.map((v) => v.version)) : 0;
    if (version === latestVersion) {
      await refreshAll();
      setHistoricalFiles(null);
      setViewingHistorical(false);
    } else {
      const at = await api.getRequirementsAtVersion(routeOrgId, projectId, `v${version}`);
      if (at?.files) {
        setHistoricalFiles(at.files);
        setCurrentVersion(version);
        setViewingHistorical(true);
        // Snap active to a file that exists in the snapshot.
        const keys = Object.keys(at.files);
        if (!activePath || at.files[activePath] === undefined) {
          setActivePath(keys.includes(REQUIREMENTS_MAIN_FILE) ? REQUIREMENTS_MAIN_FILE : (keys[0] ?? null));
        }
      }
    }
  };

  const handleDiscard = async () => {
    if (!projectId) return;
    setIsDiscarding(true);
    const bundle = await api.discardRequirements(routeOrgId, projectId);
    if (bundle) {
      setSavedFiles(bundle.files ?? {});
      setLiveContents(bundle.files ?? {});
      setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
      if (bundle.versions) setVersions(bundle.versions);
      setCurrentVersion(bundle.version ?? 0);
      userTypedRef.current = false;
      clearAllDrafts(routeOrgId, projectId);
      setSaveStatus('idle');
    }
    setIsDiscarding(false);
  };

  const handleRestoreLocal = async () => {
    if (!projectId || !restorePromptFor) return;
    const { filename, localDraft } = restorePromptFor;
    setRestorePromptFor(null);
    userTypedRef.current = true;
    setLiveContents((prev) => ({ ...prev, [filename]: localDraft }));
    if (filename === activePath) editorRef.current?.setActiveMarkdown(localDraft);
    setSaveStatus('saving');
    try {
      await api.updateRequirementFile(routeOrgId, projectId, filename, localDraft);
      setSavedFiles((prev) => ({ ...prev, [filename]: localDraft }));
      saveDraft(routeOrgId, projectId, filename, {
        draft: localDraft,
        baseServerContent: localDraft,
        savedAt: Date.now(),
      });
      setSaveStatus('saved');
    } catch {
      setSaveStatus('error');
    }
  };

  const handleDiscardLocal = () => {
    if (!projectId || !restorePromptFor) return;
    clearDraft(routeOrgId, projectId, restorePromptFor.filename);
    setRestorePromptFor(null);
  };

  // -- File operations: add / rename / delete ------------------------------

  const addFileMenuItems: AddFileMenuItem[] = useMemo(() => {
    const existing = Object.keys(savedFiles);
    return DOCUMENT_TYPES.filter((t) => !t.protected).map((t) => {
      const disabled = t.unique && existing.some((n) => documentTypeForFile(n)?.id === t.id);
      return {
        id: t.id,
        label: t.label,
        description: t.description,
        disabled,
      };
    });
  }, [savedFiles]);

  const handleAddFile = useCallback(
    (typeId?: string) => {
      if (!projectId || !typeId) return undefined;
      const type = getDocumentType(typeId);
      if (!type) return undefined;
      const filename = nextFilenameFor(type, Object.keys(savedFiles));
      // Optimistically create with empty content; server PUT happens via auto-save when user types.
      const initial = `# ${type.label}\n\nGenerate from existing documents using the Sparkles button above.`;
      setLiveContents((prev) => ({ ...prev, [filename]: initial }));
      setSavedFiles((prev) => ({ ...prev, [filename]: '' }));
      setActivePath(filename);
      // Persist the empty file so the directory exists on disk.
      api
        .updateRequirementFile(routeOrgId, projectId, filename, initial)
        .then((bundle) => {
          if (bundle?.files) {
            setSavedFiles(bundle.files);
            if (bundle.versions) setVersions(bundle.versions);
            setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
          }
        });
      return filename;
    },
    [routeOrgId, projectId, savedFiles],
  );

  const handleDelete = useCallback(
    async (path: string) => {
      if (!projectId) return;
      if (path === REQUIREMENTS_MAIN_FILE) return;
      const bundle = await api.deleteRequirementFile(routeOrgId, projectId, path);
      if (bundle?.files) {
        setSavedFiles(bundle.files);
        setLiveContents(bundle.files);
        if (bundle.versions) setVersions(bundle.versions);
        setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
      }
      if (activePath === path) {
        setActivePath(REQUIREMENTS_MAIN_FILE);
      }
      clearDraft(routeOrgId, projectId, path);
    },
    [routeOrgId, projectId, activePath],
  );

  const handleRename = useCallback(
    async (oldPath: string, newPath: string) => {
      if (!projectId) return;
      if (oldPath === REQUIREMENTS_MAIN_FILE) return; // protected
      if (oldPath === newPath) return;
      const content = liveContents[oldPath] ?? savedFiles[oldPath] ?? '';
      // Rename = create new + delete old, atomically from the user's view.
      // The auto-tag flow on save will commit both moves as one tag bump.
      await api.updateRequirementFile(routeOrgId, projectId, newPath, content);
      await api.deleteRequirementFile(routeOrgId, projectId, oldPath);
      const bundle = await api.getRequirements(routeOrgId, projectId);
      if (bundle?.files) {
        setSavedFiles(bundle.files);
        setLiveContents(bundle.files);
        if (bundle.versions) setVersions(bundle.versions);
        setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
      }
      if (activePath === oldPath) setActivePath(newPath);
      clearDraft(routeOrgId, projectId, oldPath);
    },
    [routeOrgId, projectId, liveContents, savedFiles, activePath],
  );

  // -- Generate-from-sources for the active file ---------------------------

  const activeDocType: DocumentType | undefined =
    activePath ? documentTypeForFile(activePath) : undefined;

  const generateActive = async () => {
    if (!projectId || !activePath || !activeDocType?.generationSkillId) return;
    setGeneratingFile(activePath);
    setStreamError(null);

    const filename = activePath;
    const skillId = activeDocType.generationSkillId;
    let accumulated = '';
    const ok = await api.generateRequirementFile(
      routeOrgId,
      projectId,
      filename,
      {
        skillId,
        sources: activeDocType.generationSourceFiles,
      },
      (delta) => {
        if (!delta) return;
        accumulated += delta;
        setLiveContents((prev) => ({ ...prev, [filename]: accumulated }));
        editorRef.current?.setActiveMarkdown(accumulated);
      },
    );
    setGeneratingFile(null);
    if (ok) {
      const bundle = await api.getRequirements(routeOrgId, projectId);
      if (bundle?.files) {
        setSavedFiles(bundle.files);
        if (bundle.versions) setVersions(bundle.versions);
        setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
      }
    } else {
      setStreamError(`Failed to generate ${filename}.`);
    }
  };

  // -- Publish (Save & Proceed) --------------------------------------------

  const handlePublish = async () => {
    if (!projectId) return;
    // Flush the active file's buffer so the server has the very latest content.
    if (activePath) {
      const md = editorRef.current?.getActiveMarkdown() ?? liveContents[activePath] ?? '';
      if (md) {
        await api.updateRequirementFile(routeOrgId, projectId, activePath, md);
      }
    }
    setPublishing(true);
    setPublishError(null);
    try {
      await api.saveRequirements(routeOrgId, projectId);
      clearAllDrafts(routeOrgId, projectId);
      setSaveStatus('idle');
      const existingDesign = await api.getDesign(routeOrgId, projectId);
      const regenerate = !!existingDesign && existingDesign.status !== 'none';
      navigate(projectArchitecturePath(routeOrgId, projectId), {
        state: { fromRequirements: true, regenerate },
      });
    } catch (err) {
      setPublishError(err instanceof ApiError ? err.message : 'Failed to publish. Please try again.');
      setPublishing(false);
    }
  };

  // -- Render --------------------------------------------------------------

  if (loading) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <CircularProgress size={48} sx={{ mb: 3 }} />
          <Typography variant="h6" color="text.secondary">Loading requirements...</Typography>
        </Box>
      </PageContent>
    );
  }

  // Files presented to the explorer:
  //   - viewingHistorical → snapshot at selected version
  //   - otherwise → the live buffers (fall back to savedFiles for un-typed files)
  const explorerFiles = viewingHistorical && historicalFiles
    ? historicalFiles
    : Object.fromEntries(
        Object.keys(savedFiles).map((k) => [k, liveContents[k] ?? savedFiles[k] ?? '']),
      );

  if (Object.keys(explorerFiles).length === 0 && !streamingMain && !connected) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <Typography variant="h6" color="text.secondary">No requirements generated yet.</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Go to the prompt page to generate requirements from a description.
          </Typography>
        </Box>
      </PageContent>
    );
  }

  const editorCollab: CollabConfig | undefined =
    connected && !viewingHistorical ? collabConfig : undefined;

  const canShowDiff = hasUnsavedChanges && lastTaggedActive !== null && !viewingHistorical && !streamingMain;
  const renderDiff = showDiff && canShowDiff;

  const generateLabel = activeDocType?.generationSourceFiles?.length
    ? `Generate from ${activeDocType.generationSourceFiles.join(', ')}`
    : 'Generate';

  const showGenerate =
    !!activeDocType?.generationSkillId &&
    !viewingHistorical &&
    !streamingMain &&
    activeDocType.id !== 'requirements'; // bootstrap is only via the prompt flow

  return (
    <PageContent fullWidth noPadding sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      {/* Header / toolbar */}
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
            <Typography variant="h4">Requirements</Typography>
            {versions.length > 0 && (
              <VersionSelector
                versions={versions}
                currentVersion={currentVersion}
                onVersionSelect={handleVersionSelect}
                isHistorical={viewingHistorical}
                hasUnsavedChanges={hasUnsavedChanges}
                onDiscard={handleDiscard}
                isDiscarding={isDiscarding}
              />
            )}
            {canShowDiff && (
              <Button
                variant={renderDiff ? 'contained' : 'outlined'}
                color={renderDiff ? 'primary' : 'inherit'}
                size="small"
                startIcon={<GitCompare size={16} />}
                onClick={() => setShowDiff((v) => !v)}
                sx={{ minWidth: 'auto' }}
              >
                Diff
              </Button>
            )}
            {streamingMain && (
              <Typography variant="caption" color="text.secondary">
                Generating requirements from your specification...
              </Typography>
            )}
          </Stack>
        </Box>

        {showGenerate && (
          <Tooltip title={generateLabel}>
            <span>
              <Button
                variant="outlined"
                size="small"
                startIcon={
                  generatingFile === activePath ? <CircularProgress size={14} /> : <Sparkles size={16} />
                }
                disabled={generatingFile === activePath}
                onClick={generateActive}
              >
                {generatingFile === activePath ? 'Generating...' : 'Generate'}
              </Button>
            </span>
          </Tooltip>
        )}

        {repoUrl && (
          <Button
            variant="outlined"
            size="small"
            startIcon={<GitHub size={16} />}
            onClick={() => window.open(repoUrl, '_blank', 'noopener,noreferrer')}
          >
            View Repo
          </Button>
        )}

        {!viewingHistorical && !streamingMain && (
          <>
            <Divider orientation="vertical" flexItem />
            <Button
              variant="contained"
              size="small"
              startIcon={publishing ? <CircularProgress size={14} color="inherit" /> : <Rocket size={16} />}
              disabled={publishing || Object.keys(explorerFiles).length === 0}
              onClick={handlePublish}
            >
              {publishing ? 'Publishing...' : 'Publish'}
            </Button>
          </>
        )}
        {streamingMain && (
          <Stack direction="row" alignItems="center" gap={1}>
            <CircularProgress size={16} />
            <Typography variant="body2" color="text.secondary">
              Generating...
            </Typography>
          </Stack>
        )}
      </Box>

      {(streamError || publishError) && (
        <Box sx={{ px: 3, py: 1, borderBottom: 1, borderColor: 'divider', flexShrink: 0 }}>
          {streamError && (
            <Typography variant="body2" color="error">{streamError}</Typography>
          )}
          {publishError && (
            <Typography variant="body2" color="error">{publishError}</Typography>
          )}
        </Box>
      )}

      {restorePromptFor && (
        <Box
          sx={{
            px: 3,
            py: 1.25,
            borderBottom: 1,
            borderColor: 'divider',
            bgcolor: 'warning.lighter',
            display: 'flex',
            alignItems: 'center',
            gap: 2,
            flexShrink: 0,
          }}
        >
          <Typography variant="body2" sx={{ flexGrow: 1 }}>
            Found an unsaved local draft of <strong>{restorePromptFor.filename}</strong> from{' '}
            {formatRelative(restorePromptFor.savedAt)} that diverges from the latest server content.
          </Typography>
          <Button size="small" variant="outlined" onClick={handleDiscardLocal}>Discard local</Button>
          <Button size="small" variant="contained" onClick={handleRestoreLocal}>Use local</Button>
        </Box>
      )}

      <Box sx={{ flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <Box sx={{ flex: 1, minHeight: 0, display: 'flex' }}>
          <Explorer
            files={explorerFiles}
            activePath={activePath}
            onActivePathChange={setActivePath}
            onFileChange={(path: string, md: string) => {
              userTypedRef.current = true;
              setLiveContents((prev) => ({ ...prev, [path]: md }));
            }}
            onAddFile={handleAddFile}
            addFileMenu={{ items: addFileMenuItems }}
            onDelete={handleDelete}
            onRename={handleRename}
            editorProps={{
              readOnly: viewingHistorical || streamingMain || generatingFile === activePath,
              showToolbar: !viewingHistorical && !streamingMain,
              toolbarRightContent: roomId ? (
                <CollabAwarenessBar connected={connected} peers={peers} inToolbar />
              ) : undefined,
              collab: editorCollab,
              baseMarkdown: renderDiff ? lastTaggedActive ?? undefined : undefined,
            }}
            editorRef={editorRef}
          />
        </Box>
      </Box>
    </PageContent>
  );
}
