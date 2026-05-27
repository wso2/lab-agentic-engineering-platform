import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  InputAdornment,
  Stack,
  TextField,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import { Edit2, Eye, Package, Plus, RefreshCw, Search, Sparkles, Trash2 } from '@wso2/oxygen-ui-icons-react';
import { orgSkillsApi, SkillApiError, type SkillKind, type SkillSummary } from '../services/api/orgSkills';
import { useOrgSkills, orgSkillsQueryKey } from '../hooks/useOrgSkills';
import { kindChipColor, kindLabel } from '../components/skills/skillKind';
import SkillViewer from '../components/skills/SkillViewer';
import SkillEditor from '../components/skills/SkillEditor';
import SkillImportDialog from '../components/skills/SkillImportDialog';

const GROUP_ORDER: { kind: SkillKind; heading: string; blurb: string }[] = [
  { kind: 'builtin', heading: 'Built-in', blurb: 'Shipped with the platform. Read-only — view to inspect the body.' },
  { kind: 'custom', heading: 'Custom (org-authored)', blurb: 'Authored from scratch by your organization.' },
  { kind: 'imported', heading: 'Imported', blurb: 'Uploaded AgentSkills directories from the ecosystem.' },
];

/**
 * OrgSkillsSettings — Settings → Skills catalogue surface.
 *
 * Lists built-in (read-only), custom, and imported skills; supports
 * view (all kinds), create/edit/delete (custom), import + delete
 * (imported). See docs/design/skills-system.md > "UI sketch".
 */
export default function OrgSkillsSettings() {
  const { orgId } = useParams();
  const theme = useTheme();
  const queryClient = useQueryClient();
  const { data: skills, isLoading, error, refetch } = useOrgSkills(orgId);

  const [filter, setFilter] = useState('');
  const [viewName, setViewName] = useState<string | null>(null);
  const [editName, setEditName] = useState<string | null>(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<SkillSummary | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: orgSkillsQueryKey(orgId) });

  const grouped = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const match = (s: SkillSummary) =>
      !q || s.name.toLowerCase().includes(q) || s.description.toLowerCase().includes(q);
    const out: Record<SkillKind, SkillSummary[]> = { builtin: [], custom: [], imported: [] };
    for (const s of skills ?? []) {
      if (match(s)) out[s.kind].push(s);
    }
    return out;
  }, [skills, filter]);

  const openCreate = () => {
    setEditName(null);
    setEditorOpen(true);
  };
  const openEdit = (name: string) => {
    setEditName(name);
    setEditorOpen(true);
  };

  const confirmDelete = async () => {
    if (!deleteTarget || !orgId) return;
    setDeleting(true);
    setDeleteError(null);
    try {
      await orgSkillsApi.remove(orgId, deleteTarget.name);
      setDeleteTarget(null);
      invalidate();
    } catch (e) {
      if (e instanceof SkillApiError && e.code === 'IMPORTED_SKILL_IN_USE') {
        setDeleteError('This imported skill is referenced by in-flight tasks and cannot be deleted yet.');
      } else {
        setDeleteError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Box>
      <Stack direction="row" alignItems="center" gap={1.5} sx={{ mb: 1 }}>
        <Sparkles size={22} />
        <Typography variant="h5" fontWeight={700}>
          Skills
        </Typography>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3, maxWidth: 760 }}>
        Skills shape what the platform's agents emit — code patterns, conventions, and
        project layout. They do not change platform infrastructure (auth, CORS, runtime
        config, and the build pipeline are wired in code). The built-ins are read-only;
        you can author Custom skills from scratch or Import AgentSkills directories.
      </Typography>

      <Stack direction="row" gap={1.5} alignItems="center" sx={{ mb: 2 }}>
        <TextField
          size="small"
          placeholder="Filter…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          slotProps={{
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <Search size={16} />
                </InputAdornment>
              ),
            },
          }}
          sx={{ flexGrow: 1, maxWidth: 360 }}
        />
        <Box sx={{ flexGrow: 1 }} />
        <Button variant="outlined" startIcon={<Plus size={18} />} onClick={openCreate}>
          New Custom
        </Button>
        <Button variant="outlined" startIcon={<Package size={18} />} onClick={() => setImportOpen(true)}>
          Import
        </Button>
      </Stack>

      {isLoading && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
          <CircularProgress />
        </Box>
      )}
      {error && (
        <Alert
          severity="error"
          action={
            <Button onClick={() => refetch()} startIcon={<RefreshCw size={16} />}>
              Retry
            </Button>
          }
        >
          Failed to load skills: {error instanceof Error ? error.message : String(error)}
        </Alert>
      )}

      {!isLoading && !error && (
        <Stack gap={3}>
          {GROUP_ORDER.map(({ kind, heading, blurb }) => {
            const rows = grouped[kind];
            return (
              <Box key={kind}>
                <Stack direction="row" alignItems="baseline" gap={1} sx={{ mb: 1 }}>
                  <Typography variant="subtitle1" fontWeight={700}>
                    {heading}
                  </Typography>
                  <Typography variant="caption" color="text.secondary">
                    — {rows.length}
                  </Typography>
                </Stack>
                <Typography variant="caption" color="text.secondary">
                  {blurb}
                </Typography>
                {rows.length === 0 ? (
                  <Typography variant="body2" color="text.secondary" sx={{ mt: 1, fontStyle: 'italic' }}>
                    {filter ? 'No matches.' : 'None yet.'}
                  </Typography>
                ) : (
                  <Card variant="outlined" sx={{ mt: 1 }}>
                    <CardContent sx={{ p: 0 }}>
                      {rows.map((s, idx) => (
                        <Box key={s.name}>
                          {idx > 0 && <Divider />}
                          <Stack
                            direction="row"
                            alignItems="center"
                            gap={1.5}
                            sx={{ px: 2, py: 1.5, '&:hover': { bgcolor: theme.palette.action.hover } }}
                          >
                            <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                              <Stack direction="row" alignItems="center" gap={1}>
                                <Typography variant="body1" fontWeight={600}>
                                  {s.name}
                                </Typography>
                                <Chip size="small" color={kindChipColor(s.kind)} label={kindLabel(s.kind)} />
                                <Chip size="small" variant="outlined" label={`v${s.version}`} />
                                {!s.editable && <Chip size="small" variant="outlined" label="read-only" />}
                              </Stack>
                              <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
                                {s.description}
                              </Typography>
                            </Box>
                            <Stack direction="row" gap={0.5} sx={{ flexShrink: 0 }}>
                              <Button size="small" startIcon={<Eye size={16} />} onClick={() => setViewName(s.name)}>
                                View
                              </Button>
                              {s.kind === 'custom' && (
                                <Button size="small" startIcon={<Edit2 size={16} />} onClick={() => openEdit(s.name)}>
                                  Edit
                                </Button>
                              )}
                              {s.editable && (
                                <Button
                                  size="small"
                                  color="error"
                                  startIcon={<Trash2 size={16} />}
                                  onClick={() => {
                                    setDeleteError(null);
                                    setDeleteTarget(s);
                                  }}
                                >
                                  Delete
                                </Button>
                              )}
                            </Stack>
                          </Stack>
                        </Box>
                      ))}
                    </CardContent>
                  </Card>
                )}
              </Box>
            );
          })}
        </Stack>
      )}

      {/* Dialogs */}
      {orgId && (
        <>
          <SkillViewer orgHandle={orgId} name={viewName} open={!!viewName} onClose={() => setViewName(null)} />
          <SkillEditor
            orgHandle={orgId}
            editName={editName}
            open={editorOpen}
            onClose={() => setEditorOpen(false)}
            onSaved={() => invalidate()}
          />
          <SkillImportDialog
            orgHandle={orgId}
            open={importOpen}
            onClose={() => setImportOpen(false)}
            onImported={() => invalidate()}
          />
          <Dialog
            open={!!deleteTarget}
            onClose={deleting ? undefined : () => setDeleteTarget(null)}
            maxWidth="xs"
            fullWidth
            slotProps={{ paper: { sx: { backgroundColor: 'background.default', backgroundImage: 'none', opacity: 1, backdropFilter: 'none' } } }}
          >
            <DialogTitle>Delete {deleteTarget?.name}?</DialogTitle>
            <DialogContent>
              <Typography variant="body2">
                This removes the skill from your organization's catalogue. In-flight tasks that
                already snapshotted it keep their frozen copy.
              </Typography>
              {deleteError && (
                <Alert severity="error" sx={{ mt: 2 }}>
                  {deleteError}
                </Alert>
              )}
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setDeleteTarget(null)} disabled={deleting}>
                Cancel
              </Button>
              <Button
                variant="contained"
                color="error"
                onClick={confirmDelete}
                disabled={deleting}
                startIcon={deleting ? <CircularProgress size={16} /> : <Trash2 size={16} />}
              >
                {deleting ? 'Deleting…' : 'Delete'}
              </Button>
            </DialogActions>
          </Dialog>
        </>
      )}
    </Box>
  );
}
