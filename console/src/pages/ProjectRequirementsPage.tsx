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
import { MdDiffViewer, countLineChanges, type CollabConfig } from '@asdlc/md-editor';
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
  type ModifiedFileEntry,
  clearSessionBaseline,
  getModifiedFiles,
  getSessionBaseline,
  removeModifiedFile,
  subscribeChatStore,
  subscribeRequirementsPageEvent,
} from '../services/chatStore';
import ChatModifiedBanner from '../components/ChatModifiedBanner';
import {
  DOCUMENT_TYPES,
  documentTypeForFile,
  getDocumentType,
  nextFilenameFor,
  toTitleCase,
  type DocumentType,
} from '../lib/documentTypes';
import { tryDslToExcalidraw, type DslKind } from '@asdlc/excalidraw-dsl';

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
  // Files waiting for their first content delta after the user added them
  // (or clicked Generate). Sidebar surfaces these with a spinner + label.
  const [pendingPaths, setPendingPaths] = useState<Set<string>>(new Set());
  // Files the chat agent is currently editing this turn. Drives the
  // soft-lock readOnly state on the editor + a busy dot in the explorer.
  const [chatBusyPaths, setChatBusyPaths] = useState<Set<string>>(new Set());
  const [chatTurnInFlight, setChatTurnInFlight] = useState(false);
  // Files in the chat session's modified set (sourced from chatStore).
  // Triggers the chat-modified banner on the active file.
  const [chatModifiedFiles, setChatModifiedFiles] = useState<Record<string, ModifiedFileEntry>>({});
  // Per-file Revert in flight — used to disable the banner action.
  const [revertingPaths, setRevertingPaths] = useState<Set<string>>(new Set());
  /**
   * Baseline content per chat-modified file, keyed by filename. Populated
   * lazily as files enter the modified set so the inline diff view can
   * render without flicker on file-switch. `null` is a sentinel for
   * "fetched and the file did not exist at baseline" (tombstone — diff
   * shows everything as additions).
   */
  const [baselineContents, setBaselineContents] = useState<Record<string, string | null>>({});
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
    // While a chat turn writes to the working tree, the BFF holds an
    // advisory lock and any concurrent PUT (the collab save loop's tick)
    // will 409 anyway — suppress the call.
    paused: chatTurnInFlight,
  });

  const collabConfig: CollabConfig | undefined = useMemo(() => {
    if (!ydoc || !provider || !user) return undefined;
    return { ydoc, provider, user };
  }, [ydoc, provider, user]);

  // -- Chat → page sync ----------------------------------------------------
  // Subscribe to the requirementsPageEvent bus the chat panel publishes
  // to. We mirror file writes into savedFiles + liveContents (so the
  // editor refreshes from BFF-authoritative content), track busy paths
  // (for the explorer's spinner + the editor's soft lock), and toggle
  // `chatTurnInFlight` (which pauses the Yjs save loop).
  useEffect(() => {
    if (!projectId) return;
    return subscribeRequirementsPageEvent((event) => {
      if (event.kind === 'turnStarted') {
        if (event.orgId !== routeOrgId || event.projectId !== projectId) return;
        setChatTurnInFlight(true);
      } else if (event.kind === 'turnEnded') {
        if (event.orgId !== routeOrgId || event.projectId !== projectId) return;
        setChatTurnInFlight(false);
        setChatBusyPaths(new Set());
      } else if (event.kind === 'busyPathsChanged') {
        if (event.orgId !== routeOrgId || event.projectId !== projectId) return;
        setChatBusyPaths(event.paths);
      } else if (event.kind === 'fileWritten') {
        // BFF persisted a tool's edit; mirror into both maps so the
        // active editor refreshes from disk.
        const update = (prev: Record<string, string>): Record<string, string> => {
          const next = { ...prev };
          if (event.content === undefined) {
            // Delete: drop the key entirely.
            delete next[event.filename];
          } else {
            next[event.filename] = event.content;
          }
          if (event.siblings) {
            for (const [k, v] of Object.entries(event.siblings)) {
              next[k] = v;
            }
          }
          return next;
        };
        setSavedFiles(update);
        setLiveContents(update);
        // Reseed the active editor buffer if the active file changed
        // (otherwise the in-buffer Yjs state would shadow the new disk
        // content on the next save tick).
        if (activePath === event.filename && event.content !== undefined) {
          editorRef.current?.setActiveMarkdown(event.content);
        }
      }
    });
  }, [routeOrgId, projectId, activePath]);

  // Mirror chatStore's modified-files map so the chat-modified banner
  // re-renders when the chat agent writes a file or the user
  // Accepts / Reverts.
  useEffect(() => {
    if (!projectId) return;
    const apply = () => setChatModifiedFiles(getModifiedFiles(routeOrgId, projectId));
    apply();
    return subscribeChatStore(apply);
  }, [routeOrgId, projectId]);

  // ---- Chat-modified handlers --------------------------------------------

  // Lazily fetch the baseline content for every file in the chat-modified
  // set. We cache by filename so switching between files doesn't re-fetch
  // (the snapshot is immutable for the session). Tombstone files (created
  // by chat) cache as `null` — the diff viewer treats that as "all added".
  useEffect(() => {
    if (!projectId) return;
    const baseline = getSessionBaseline(routeOrgId, projectId);
    if (!baseline) return;
    const pending = Object.keys(chatModifiedFiles).filter(
      (filename) => baselineContents[filename] === undefined,
    );
    if (pending.length === 0) return;
    let cancelled = false;
    (async () => {
      const fetched = await Promise.all(
        pending.map(async (filename) => {
          const file = await api.getRequirementsBaselineFile(
            routeOrgId,
            projectId,
            baseline.snapshotId,
            filename,
          );
          return [filename, file?.existed ? file.content : null] as const;
        }),
      );
      if (cancelled) return;
      setBaselineContents((prev) => {
        const next = { ...prev };
        for (const [filename, content] of fetched) {
          next[filename] = content;
        }
        return next;
      });
    })();
    return () => {
      cancelled = true;
    };
  }, [routeOrgId, projectId, chatModifiedFiles, baselineContents]);

  // Garbage-collect baselineContents when files leave the modified set
  // (Accept / Revert / clear). Keeps the map honest so a stale entry
  // can't survive a session reset.
  useEffect(() => {
    setBaselineContents((prev) => {
      let changed = false;
      const next: Record<string, string | null> = {};
      for (const [filename, value] of Object.entries(prev)) {
        if (chatModifiedFiles[filename]) {
          next[filename] = value;
        } else {
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [chatModifiedFiles]);

  // Per-file +N/-M counts for the explorer tree. Recomputed only when
  // the modified set, the cached baselines, or the live content of those
  // files changes. Skipped entries (`undefined` baseline = still
  // fetching) are simply omitted so the sidebar chip shows nothing
  // until the diff is computable.
  const chatModifiedCounts = useMemo(() => {
    const map = new Map<string, { added: number; removed: number }>();
    for (const filename of Object.keys(chatModifiedFiles)) {
      const baseline = baselineContents[filename];
      if (baseline === undefined) continue;
      const current = liveContents[filename] ?? savedFiles[filename] ?? '';
      const counts = countLineChanges(baseline ?? '', current);
      map.set(filename, counts);
    }
    return map;
  }, [chatModifiedFiles, baselineContents, liveContents, savedFiles]);

  /**
   * Renders the inline diff view for files in the chat-modified set, in
   * place of the default markdown editor. Both the baseline and the
   * current content are markdown-only — canvas-backed files (`.excalidraw`)
   * fall back to the regular editor pipeline.
   */
  const renderChatModifiedFile = useCallback(
    (path: string): React.ReactNode | undefined => {
      const entry = chatModifiedFiles[path];
      if (!entry) return undefined;
      if (/\.excalidraw$/i.test(path)) return undefined;
      // Render the diff live: every subsequent tool result re-fires
      // `fileWritten`, which updates `liveContents[path]`, which flows
      // into MdDiffViewer's `newMarkdown` and recomputes the diff doc.
      // Each tool inside a turn lands atomically (no mid-write content),
      // so the user sees the cumulative diff grow as the agent works.
      const baseline = baselineContents[path];
      const current = liveContents[path] ?? savedFiles[path] ?? '';
      if (baseline === undefined) {
        return (
          <Box
            sx={{
              flex: 1,
              minHeight: 0,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              gap: 1.5,
              color: 'text.secondary',
            }}
          >
            <CircularProgress size={18} />
            <Typography variant="body2">Loading diff…</Typography>
          </Box>
        );
      }
      return (
        <Box
          data-testid="chat-diff-view"
          data-filename={path}
          sx={{ flex: 1, minHeight: 0, overflow: 'auto', p: 2, bgcolor: 'background.default' }}
        >
          <MdDiffViewer oldMarkdown={baseline ?? ''} newMarkdown={current} />
        </Box>
      );
    },
    [chatModifiedFiles, baselineContents, liveContents, savedFiles],
  );

  const handleAcceptChatFile = useCallback((filename: string) => {
    if (!projectId) return;
    const key = { orgId: routeOrgId, projectId };
    removeModifiedFile(key, filename);
    // If this was the last file in the modified set, drop the baseline
    // snapshot on the server side too. Read the fresh state after the
    // remove (cache is updated synchronously inside removeModifiedFile).
    const remaining = getModifiedFiles(routeOrgId, projectId);
    if (Object.keys(remaining).length === 0) {
      const baseline = getSessionBaseline(routeOrgId, projectId);
      if (baseline) {
        void api.dropRequirementsBaseline(routeOrgId, projectId, baseline.snapshotId);
        clearSessionBaseline(key);
      }
    }
  }, [routeOrgId, projectId]);

  const handleRevertChatFile = useCallback(async (filename: string) => {
    if (!projectId) return;
    const baseline = getSessionBaseline(routeOrgId, projectId);
    if (!baseline) {
      // Without a baseline we can't revert — fall through to Accept-style
      // clear so the banner doesn't hang.
      handleAcceptChatFile(filename);
      return;
    }
    setRevertingPaths((prev) => {
      const next = new Set(prev);
      next.add(filename);
      return next;
    });
    try {
      const ok = await api.revertRequirementsBaselineFile(
        routeOrgId,
        projectId,
        baseline.snapshotId,
        filename,
      );
      if (!ok) {
        setPublishError(`Failed to revert ${filename} — see chat logs.`);
        return;
      }
      // Refresh the bundle so the editor + sidebar reflect the rollback.
      const bundle = await api.getRequirements(routeOrgId, projectId);
      if (bundle?.files) {
        setSavedFiles(bundle.files);
        setLiveContents(bundle.files);
        if (bundle.versions) setVersions(bundle.versions);
        setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
      }
      // Reseed the editor if the user was viewing the reverted file.
      if (activePath === filename) {
        const fresh = bundle?.files?.[filename];
        if (fresh !== undefined) editorRef.current?.setActiveMarkdown(fresh);
      }
      // Clear from modified set; if last, drop baseline.
      handleAcceptChatFile(filename);
    } finally {
      setRevertingPaths((prev) => {
        if (!prev.has(filename)) return prev;
        const next = new Set(prev);
        next.delete(filename);
        return next;
      });
    }
  }, [routeOrgId, projectId, activePath, handleAcceptChatFile]);

  // After a turn ends, refresh the bundle so version + has-unsaved indicators
  // re-derive from the new working tree.
  useEffect(() => {
    if (chatTurnInFlight) return;
    if (!projectId) return;
    // Best-effort: pull the latest bundle on turn end. No-op if nothing
    // changed.
    void refreshAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatTurnInFlight]);

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
      // Canvas-backed files (wireframes / domain-model) are view-only on
      // this page — the user can only update them via Generate. Any
      // draft that survived in localStorage is a relic of the spurious
      // initial Excalidraw onChange (pre-readOnly-gate); discard it so
      // it can't shadow a fresh generation on the next page load.
      if (/\.excalidraw$/i.test(filename)) {
        clearDraft(routeOrgId, projectId, filename);
        continue;
      }
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
    // Each artifact type appears in the picker at most once. As soon as
    // the user adds it, the type drops out of the list so the picker
    // doesn't grow stale with already-added options.
    const existingTypeIds = new Set(
      Object.keys(savedFiles)
        .map((n) => documentTypeForFile(n)?.id)
        .filter((id): id is NonNullable<typeof id> => Boolean(id)),
    );
    return DOCUMENT_TYPES
      .filter((t) => !t.protected && !existingTypeIds.has(t.id))
      .map((t) => ({
        id: t.id,
        label: t.label,
        description: t.description,
      }));
  }, [savedFiles]);

  const handleAddFile = useCallback(
    (typeId?: string) => {
      if (!projectId || !typeId) return undefined;
      const type = getDocumentType(typeId);
      if (!type) return undefined;
      const filename = nextFilenameFor(type, Object.keys(savedFiles));
      const willAutoGenerate = !!type.generationSkillId;
      // Optimistically create with starter content; server PUT happens via auto-save when user types.
      // Markdown gets a heading + hint; canvas-backed types start blank
      // because the editor parses empty string as a blank scene and any
      // markdown placeholder would be invalid JSON. When we auto-generate
      // (the common case from the Add document dialog) seed the markdown
      // body empty too so the generated stream is the first content the
      // user ever sees on this file.
      const initial = type.extension === '.excalidraw'
        ? ''
        : willAutoGenerate
          ? ''
          : `# ${type.label}\n\nGenerate from existing documents using the Sparkles button above.`;
      setLiveContents((prev) => ({ ...prev, [filename]: initial }));
      setSavedFiles((prev) => ({ ...prev, [filename]: '' }));
      setActivePath(filename);
      // Mark pending up-front so the spinner shows from the click — no
      // gap between "file appears in sidebar" and "generation starts".
      if (willAutoGenerate) markPending(filename);
      // Persist the empty file so the directory exists on disk, then kick
      // off generation immediately.
      api
        .updateRequirementFile(routeOrgId, projectId, filename, initial)
        .then((bundle) => {
          if (bundle?.files) {
            setSavedFiles(bundle.files);
            if (bundle.versions) setVersions(bundle.versions);
            setHasUnsavedChanges(bundle.hasUnsavedChanges ?? false);
          }
          if (willAutoGenerate) void generateFor(filename, type);
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

  const markPending = useCallback((path: string) => {
    setPendingPaths((prev) => {
      if (prev.has(path)) return prev;
      const next = new Set(prev);
      next.add(path);
      return next;
    });
  }, []);

  const clearPending = useCallback((path: string) => {
    setPendingPaths((prev) => {
      if (!prev.has(path)) return prev;
      const next = new Set(prev);
      next.delete(path);
      return next;
    });
  }, []);

  const generateFor = useCallback(
    async (filename: string, docType: DocumentType) => {
      if (!projectId || !docType.generationSkillId) return;
      setGeneratingFile(filename);
      markPending(filename);
      setStreamError(null);

      const skillId = docType.generationSkillId;
      // Excalidraw-backed skills (wireframes / domain-model) stream a tiny DSL
      // and emit a final `replace:true` delta with the converted JSON. While
      // the DSL is in flight we run the same conversion client-side for a
      // best-effort live preview on the canvas — partial DSL that doesn't
      // parse yet just leaves the canvas at its previous state.
      const excalidrawKind: DslKind | null =
        filename.toLowerCase().endsWith('.excalidraw')
          ? docType.id === 'wireframes'
            ? 'wireframes'
            : docType.id === 'domain-model'
              ? 'domain-model'
              : null
          : null;
      let accumulated = '';
      // First *visible* delta clears the pending spinner. For excalidraw
      // it's the first DSL frame that parses; for markdown it's the first
      // non-empty delta.
      let firstContent = false;
      const noteFirstContent = () => {
        if (!firstContent) {
          firstContent = true;
          clearPending(filename);
        }
      };
      const ok = await api.generateRequirementFile(
        routeOrgId,
        projectId,
        filename,
        {
          skillId,
          sources: docType.generationSourceFiles,
        },
        (delta, opts) => {
          if (!delta) return;
          if (opts?.replace) {
            accumulated = delta;
          } else {
            accumulated += delta;
          }
          if (excalidrawKind) {
            if (opts?.replace) {
              // Final converted Excalidraw JSON from the agents-service.
              setLiveContents((prev) => ({ ...prev, [filename]: delta }));
              noteFirstContent();
            } else {
              const attempt = tryDslToExcalidraw(excalidrawKind, accumulated);
              if (attempt.ok) {
                setLiveContents((prev) => ({ ...prev, [filename]: attempt.json }));
                noteFirstContent();
              }
            }
            return;
          }
          setLiveContents((prev) => ({ ...prev, [filename]: accumulated }));
          editorRef.current?.setActiveMarkdown(accumulated);
          noteFirstContent();
        },
      );
      setGeneratingFile(null);
      clearPending(filename);
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
    },
    [routeOrgId, projectId, markPending, clearPending],
  );

  const generateActive = () => {
    if (!activePath || !activeDocType) return;
    void generateFor(activePath, activeDocType);
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
  // The `.dsl` sibling files persisted alongside wireframes / domain-model
  // canvases are an internal source format; hide them from the sidebar so
  // the user only sees the rendered `.excalidraw`.
  const hideDsl = (m: Record<string, string>): Record<string, string> =>
    Object.fromEntries(Object.entries(m).filter(([k]) => !/\.dsl$/i.test(k)));
  const explorerFiles = hideDsl(
    viewingHistorical && historicalFiles
      ? historicalFiles
      : Object.fromEntries(
          Object.keys(savedFiles).map((k) => [k, liveContents[k] ?? savedFiles[k] ?? '']),
        ),
  );

  const getFileLabel = (path: string): string | undefined => {
    const type = documentTypeForFile(path);
    const base = type ? type.label : toTitleCase(path);
    // Surface a "generating…" tag in the sidebar while the file is
    // waiting for its first content delta (auto-generate on add, or
    // explicit Generate). Spinner from `pendingPaths` covers the icon
    // slot; this suffixes the label so users can read the state at a
    // glance.
    if (pendingPaths.has(path)) return `${base} — generating…`;
    return base;
  };

  // Sort the sidebar by registered document order — keeps `requirements.md`
  // at the top and lays out the generated docs (functional → NFR → stories
  // → wireframes → domain model) the way the user added them. Anything not
  // in the registry falls back to alphabetical at the end.
  const getFileSortKey = (path: string): number | undefined => {
    const type = documentTypeForFile(path);
    if (!type) return undefined;
    const idx = DOCUMENT_TYPES.findIndex((t) => t.id === type.id);
    return idx < 0 ? undefined : idx;
  };

  // Streaming bootstrap (fresh project from a prompt): the first delta hasn't
  // landed yet, so there are no files for the Explorer to render. Show a
  // dedicated "Generating…" state instead of the Explorer chrome — otherwise
  // the sidebar briefly flashes "No files" until the first delta arrives.
  if (Object.keys(explorerFiles).length === 0 && streamingMain) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
          <CircularProgress size={48} sx={{ mb: 3 }} />
          <Typography variant="h6" color="text.secondary">Generating requirements…</Typography>
        </Box>
      </PageContent>
    );
  }

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

  // Diff is markdown-only — the underlying MdEditor renders inline diff
  // decorations, which don't apply to canvas-backed (.excalidraw) files.
  const isCanvasFile = activePath?.toLowerCase().endsWith('.excalidraw') ?? false;
  const canShowDiff =
    hasUnsavedChanges &&
    lastTaggedActive !== null &&
    !viewingHistorical &&
    !streamingMain &&
    !isCanvasFile;
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

      {activePath &&
        chatModifiedFiles[activePath] &&
        !viewingHistorical &&
        !streamingMain && (
          <ChatModifiedBanner
            filename={activePath}
            busy={chatTurnInFlight}
            pending={revertingPaths.has(activePath)}
            counts={chatModifiedCounts.get(activePath)}
            onAccept={() => handleAcceptChatFile(activePath)}
            onRevert={() => void handleRevertChatFile(activePath)}
          />
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
            getFileLabel={getFileLabel}
            getFileSortKey={getFileSortKey}
            pendingPaths={
              chatBusyPaths.size === 0
                ? pendingPaths
                : new Set([...pendingPaths, ...chatBusyPaths])
            }
            chatModifiedPaths={chatModifiedCounts}
            getFileRenderer={renderChatModifiedFile}
            editorProps={{
              readOnly:
                viewingHistorical ||
                streamingMain ||
                generatingFile === activePath ||
                (activePath !== null && chatBusyPaths.has(activePath)),
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
