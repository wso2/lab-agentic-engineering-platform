import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Button,
  CircularProgress,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { Edit, Eye, GitHub, Rocket, X } from '@wso2/oxygen-ui-icons-react';
import { MdDiffViewer, type CollabConfig } from '@asdlc/md-editor';
import { MdExplorer, type MdExplorerRef } from '@asdlc/md-explorer';
import { api, ApiError } from '../services/api';
import type { ArtifactVersion } from '../services/api';
import { projectArchitecturePath } from '../lib/paths';
import { useCollabEditor } from '../hooks/useCollabEditor';
import CollabAwarenessBar from '../components/CollabAwarenessBar';
import VersionSelector from '../components/VersionSelector';
import WireframePreviewModal from '../components/WireframePreviewModal';

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export default function ProjectRequirementsPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';

  const streamPrompt = (location.state as { streamPrompt?: string } | null)?.streamPrompt ?? null;

  const [loading, setLoading] = useState(!streamPrompt);
  const [specContent, setSpecContent] = useState('');
  const [roomId, setRoomId] = useState<string | null>(null);
  const [isEditing, setIsEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [publishError, setPublishError] = useState<string | null>(null);
  const [streaming, setStreaming] = useState(!!streamPrompt);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [versions, setVersions] = useState<ArtifactVersion[]>([]);
  const [currentVersion, setCurrentVersion] = useState(0);
  const [viewingHistorical, setViewingHistorical] = useState(false);
  const [historicalContent, setHistoricalContent] = useState<string | null>(null);
  const [hasUnsavedChanges, setHasUnsavedChanges] = useState(false);
  const [isDiscarding, setIsDiscarding] = useState(false);
  const [lastTaggedContent, setLastTaggedContent] = useState<string | null>(null);
  const [preEditContent, setPreEditContent] = useState<string | null>(null);
  const [wireframeOpen, setWireframeOpen] = useState(false);
  const [repoUrl, setRepoUrl] = useState<string>('');
  const [liveContent, setLiveContent] = useState('');

  const editorRef = useRef<MdExplorerRef>(null);
  const [userName, setUserName] = useState<string | undefined>(undefined);

  const handleCollabSave = useCallback(async (val: string) => {
    if (!projectId || !val.trim()) return;
    await api.updateSpec(routeOrgId, projectId, val);
    const spec = await api.getSpec(routeOrgId, projectId);
    if (spec) {
      setHasUnsavedChanges(spec.hasUnsavedChanges ?? false);
      if (spec.versions) setVersions(spec.versions);
      setCurrentVersion(spec.version ?? 0);
    }
  }, [routeOrgId, projectId]);

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
    isEditing,
    userName,
  });

  const collabConfig: CollabConfig | undefined = useMemo(() => {
    if (!ydoc || !provider || !user) return undefined;
    return { ydoc, provider, user };
  }, [ydoc, provider, user]);

  const startedRef = useRef(false);
  const abortRef = useRef<AbortController | null>(null);
  useEffect(() => {
    if (!projectId) return;
    if (streamPrompt && !startedRef.current) {
      startedRef.current = true;
      navigate(location.pathname, { replace: true });
      setSpecContent('');
      setStreamError(null);

      const controller = new AbortController();
      abortRef.current = controller;
      api
        .generateSpec(
          routeOrgId,
          projectId,
          streamPrompt,
          (delta) => {
            if (!delta) return;
            setSpecContent((prev) => prev + delta);
          },
          controller.signal,
        )
        .then(async (ok) => {
          if (controller.signal.aborted) return;
          setStreaming(false);
          if (ok) {
            const [spec, session] = await Promise.all([
              api.getSpec(routeOrgId, projectId!),
              api.getCollabSession(routeOrgId, projectId!),
            ]);
            if (spec?.content) {
              setSpecContent(spec.content);
              setCurrentVersion(spec.version ?? 0);
              setHasUnsavedChanges(spec.hasUnsavedChanges ?? false);
            }
            if (spec?.versions) setVersions(spec.versions);
            if (session?.roomId) setRoomId(session.roomId);
            if (session) setUserName(session.userName || session.email || 'Anonymous');
          } else {
            setStreamError('Failed to generate requirements. Please try again.');
          }
        })
        .catch(() => {
          if (controller.signal.aborted) return;
          setStreaming(false);
          setStreamError('Failed to generate requirements. Please try again.');
        });
      return;
    }
    (async () => {
      const [spec, session] = await Promise.all([
        api.getSpec(routeOrgId, projectId),
        api.getCollabSession(routeOrgId, projectId),
      ]);
      if (spec?.content) {
        setSpecContent(spec.content);
        setCurrentVersion(spec.version ?? 0);
        setHasUnsavedChanges(spec.hasUnsavedChanges ?? false);
      }
      if (spec?.versions) setVersions(spec.versions);
      if (session?.roomId) setRoomId(session.roomId);
      if (session) setUserName(session.userName || session.email || 'Anonymous');
      setLoading(false);
    })();
  }, [streamPrompt, routeOrgId, projectId, navigate, location.pathname]);

  useEffect(() => () => abortRef.current?.abort(), []);

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

  // Fetch the last tagged version so we can diff against it when the working
  // copy has uncommitted changes.
  useEffect(() => {
    if (!projectId || !hasUnsavedChanges || versions.length === 0) {
      setLastTaggedContent(null);
      return;
    }
    const latest = Math.max(...versions.map((v) => v.version));
    let cancelled = false;
    (async () => {
      const spec = await api.getSpecAtVersion(routeOrgId, projectId, latest);
      if (!cancelled && spec?.content !== undefined) {
        setLastTaggedContent(spec.content);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [routeOrgId, projectId, hasUnsavedChanges, versions]);

  const handleVersionSelect = async (version: number) => {
    if (!projectId) return;
    const latestVersion = versions.length > 0 ? Math.max(...versions.map((v) => v.version)) : 0;
    if (version === latestVersion) {
      const spec = await api.getSpec(routeOrgId, projectId);
      if (spec?.content) {
        setSpecContent(spec.content);
        setCurrentVersion(spec.version ?? 0);
      }
      setHistoricalContent(null);
      setViewingHistorical(false);
      setIsEditing(false);
    } else {
      const spec = await api.getSpecAtVersion(routeOrgId, projectId, version);
      if (spec?.content) {
        setHistoricalContent(spec.content);
        setCurrentVersion(version);
        setViewingHistorical(true);
        setIsEditing(false);
      }
    }
  };

  const handleSave = async () => {
    if (!projectId) return;
    setSaving(true);
    const currentContent = editorRef.current?.getActiveMarkdown() ?? specContent;
    await api.updateSpec(routeOrgId, projectId, currentContent);
    const spec = await api.getSpec(routeOrgId, projectId);
    if (spec) {
      setSpecContent(spec.content ?? currentContent);
      setHasUnsavedChanges(spec.hasUnsavedChanges ?? false);
      setVersions(spec.versions ?? versions);
      setCurrentVersion(spec.version ?? 0);
    }
    setSaving(false);
    setIsEditing(false);
    setPreEditContent(null);
  };

  const handleDiscard = async () => {
    if (!projectId) return;
    setIsDiscarding(true);
    const spec = await api.discardSpecChanges(routeOrgId, projectId);
    if (spec) {
      setSpecContent(spec.content);
      setCurrentVersion(spec.version ?? 0);
      setHasUnsavedChanges(spec.hasUnsavedChanges ?? false);
      setVersions(spec.versions ?? versions);
      editorRef.current?.setActiveMarkdown(spec.content);
      setIsEditing(false);
    }
    setIsDiscarding(false);
  };

  // -- Loading state ----------------------------------------------------------

  if (loading) {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
        <CircularProgress size={48} sx={{ mb: 3 }} />
        <Typography variant="h6" color="text.secondary">Loading requirements...</Typography>
      </Box>
    );
  }

  const staticDisplay = viewingHistorical ? (historicalContent ?? '') : specContent;
  const filesContent = isEditing ? (liveContent || specContent) : staticDisplay;

  if (!staticDisplay && !viewingHistorical && !streaming && !connected) {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 12 }}>
        <Typography variant="h6" color="text.secondary">No requirements generated yet.</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          Go to the prompt page to generate requirements from a description.
        </Typography>
      </Box>
    );
  }

  const editorCollab: CollabConfig | undefined =
    connected && !viewingHistorical ? collabConfig : undefined;

  const contentForGating = isEditing ? liveContent : specContent;

  return (
    <Box sx={{ height: 'calc(100vh - 180px)', display: 'flex', flexDirection: 'column' }}>
      {/* Header */}
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 1 }}>
        <Stack direction="row" alignItems="center" gap={1.5}>
          <Typography variant="h4" fontWeight={700}>Requirements</Typography>
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
        </Stack>
        {!viewingHistorical && !streaming && (
          <Stack direction="row" alignItems="center" gap={1}>
            {isEditing ? (
              <>
                <Button
                  variant="outlined"
                  size="small"
                  startIcon={<X size={16} />}
                  onClick={() => {
                    if (preEditContent !== null) {
                      editorRef.current?.setActiveMarkdown(preEditContent);
                    }
                    setPreEditContent(null);
                    setIsEditing(false);
                  }}
                  disabled={saving}
                >
                  Cancel
                </Button>
                <Button
                  variant="contained"
                  size="small"
                  startIcon={<Edit size={16} />}
                  onClick={handleSave}
                  disabled={saving}
                >
                  {saving ? 'Saving...' : 'Save'}
                </Button>
              </>
            ) : (
              <>
                <Button
                  variant="outlined"
                  size="small"
                  startIcon={<Eye size={16} />}
                  onClick={() => setWireframeOpen(true)}
                  disabled={!contentForGating.trim()}
                >
                  Wireframe
                </Button>
                <Button
                  variant="outlined"
                  size="small"
                  startIcon={<Edit size={16} />}
                  onClick={() => {
                    setPreEditContent(editorRef.current?.getActiveMarkdown() ?? specContent);
                    setIsEditing(true);
                  }}
                >
                  Edit
                </Button>
                <Button
                  variant="contained"
                  size="small"
                  startIcon={generating ? <CircularProgress size={14} color="inherit" /> : <Rocket size={16} />}
                  disabled={generating || !contentForGating.trim()}
                  onClick={async () => {
                    if (!projectId) return;
                    const md = editorRef.current?.getActiveMarkdown() ?? specContent;
                    if (!md.trim()) return;
                    setGenerating(true);
                    setPublishError(null);
                    try {
                      await api.updateSpec(routeOrgId, projectId, md);
                      await api.saveAndProceedSpec(routeOrgId, projectId);
                      const existingDesign = await api.getDesign(routeOrgId, projectId);
                      const regenerate =
                        !!existingDesign && existingDesign.status !== 'none';
                      navigate(projectArchitecturePath(routeOrgId, projectId), {
                        state: { fromRequirements: true, regenerate },
                      });
                    } catch (err) {
                      setPublishError(err instanceof ApiError ? err.message : 'Failed to publish. Please try again.');
                      setGenerating(false);
                    }
                  }}
                >
                  {generating ? 'Publishing...' : 'Publish'}
                </Button>
              </>
            )}
          </Stack>
        )}
        {streaming && (
          <Stack direction="row" alignItems="center" gap={1}>
            <CircularProgress size={16} />
            <Typography variant="body2" color="text.secondary">
              Generating...
            </Typography>
          </Stack>
        )}
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        {streaming
          ? 'Generating requirements from your specification. This may take a moment.'
          : 'AI-generated requirements from your specification. Review and edit as needed.'}
      </Typography>
      {streamError && (
        <Typography variant="body2" color="error" sx={{ mb: 2 }}>
          {streamError}
        </Typography>
      )}
      {publishError && (
        <Typography variant="body2" color="error" sx={{ mb: 2 }}>
          {publishError}
        </Typography>
      )}

      {/* Markdown editor / diff viewer */}
      {(() => {
        const showDiff =
          !isEditing && !viewingHistorical && hasUnsavedChanges && lastTaggedContent !== null;
        const diffNew = liveContent || staticDisplay;
        return (
          <Box sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
            {showDiff && (
              <MdDiffViewer
                oldMarkdown={lastTaggedContent ?? ''}
                newMarkdown={diffNew}
                maxHeight={Math.max(320, typeof window !== 'undefined' ? window.innerHeight - 340 : 600)}
                minHeight={320}
              />
            )}
            <Box sx={{ flex: 1, minHeight: 0, display: showDiff ? 'none' : 'flex' }}>
              <MdExplorer
                files={{ Requirements: filesContent }}
                activePath="Requirements"
                onFileChange={(_: string, md: string) => setLiveContent(md)}
                editorProps={{
                  readOnly: !isEditing || viewingHistorical || streaming,
                  showToolbar: isEditing && !viewingHistorical,
                  toolbarRightContent: isEditing && roomId ? (
                    <CollabAwarenessBar connected={connected} peers={peers} inToolbar />
                  ) : undefined,
                  collab: editorCollab,
                }}
                editorRef={editorRef}
                maxHeight={Math.max(320, typeof window !== 'undefined' ? window.innerHeight - 340 : 600)}
                minHeight={320}
              />
            </Box>
          </Box>
        );
      })()}

      <WireframePreviewModal
        open={wireframeOpen}
        onClose={() => setWireframeOpen(false)}
        orgHandle={routeOrgId}
        projectId={projectId ?? ''}
      />
    </Box>
  );
}
