import { useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
  Typography,
} from '@wso2/oxygen-ui';
import {
  orgSkillsApi,
  SkillApiError,
  type SkillValidationIssue,
} from '../../services/api/orgSkills';
import { newSkillTemplate } from './skillKind';

interface SkillEditorProps {
  orgHandle: string;
  /** When set, the editor edits this existing custom skill; otherwise creates. */
  editName: string | null;
  open: boolean;
  onClose: () => void;
  onSaved: (name: string) => void;
}

/**
 * SkillEditor — create or edit a custom skill. Faithful to the file-first
 * authoring model: the user edits the full SKILL.md (frontmatter + body)
 * directly; the BFF validates it. Structured validation issues render
 * inline. The `name` is immutable on edit (a rename is delete + recreate).
 */
export default function SkillEditor({ orgHandle, editName, open, onClose, onSaved }: SkillEditorProps) {
  const isEdit = !!editName;
  const [name, setName] = useState('');
  const [skillMd, setSkillMd] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [issues, setIssues] = useState<SkillValidationIssue[]>([]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setError(null);
    setIssues([]);
    if (isEdit && editName) {
      setLoading(true);
      setName(editName);
      orgSkillsApi
        .get(orgHandle, editName)
        .then((d) => {
          if (!cancelled) setSkillMd(d.skillMd);
        })
        .catch((e) => {
          if (!cancelled) setError(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    } else {
      setName('');
      setSkillMd(newSkillTemplate(''));
    }
    return () => {
      cancelled = true;
    };
  }, [open, isEdit, editName, orgHandle]);

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    setIssues([]);
    try {
      if (isEdit && editName) {
        await orgSkillsApi.update(orgHandle, editName, { skillMd });
        onSaved(editName);
      } else {
        const created = await orgSkillsApi.create(orgHandle, { name: name.trim(), skillMd });
        onSaved(created.name);
      }
      onClose();
    } catch (e) {
      if (e instanceof SkillApiError && e.issues && e.issues.length > 0) {
        setIssues(e.issues);
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={saving ? undefined : onClose}
      maxWidth="md"
      fullWidth
      slotProps={{ paper: { sx: { backgroundColor: 'background.default', backgroundImage: 'none', opacity: 1, backdropFilter: 'none' } } }}
    >
      <DialogTitle>{isEdit ? `Edit skill: ${editName}` : 'New custom skill'}</DialogTitle>
      <DialogContent dividers>
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress size={28} />
          </Box>
        ) : (
          <Stack gap={2}>
            <Alert severity="info">
              Custom skills are authored in the on-disk AgentSkills format. Edit the full
              SKILL.md below (YAML frontmatter + body); the platform validates it on save.
            </Alert>
            {!isEdit && (
              <TextField
                label="Name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="payments-pci-handling"
                helperText="Lowercase kebab-case, ≤ 55 chars. Must match the frontmatter name."
                fullWidth
                size="small"
              />
            )}
            <TextField
              label="SKILL.md"
              value={skillMd}
              onChange={(e) => setSkillMd(e.target.value)}
              multiline
              minRows={16}
              maxRows={28}
              fullWidth
              slotProps={{ input: { sx: { fontFamily: 'monospace', fontSize: 13 } } }}
            />
            {issues.length > 0 && (
              <Alert severity="error">
                <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
                  Validation failed
                </Typography>
                <Stack gap={0.5}>
                  {issues.map((i, idx) => (
                    <Typography key={idx} variant="caption">
                      <strong>{i.code}</strong>
                      {i.path ? ` (${i.path})` : ''}: {i.message}
                    </Typography>
                  ))}
                </Stack>
              </Alert>
            )}
            {error && <Alert severity="error">{error}</Alert>}
          </Stack>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={saving}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleSave}
          disabled={saving || loading || (!isEdit && !name.trim()) || !skillMd.trim()}
          startIcon={saving ? <CircularProgress size={16} /> : undefined}
        >
          {saving ? 'Saving…' : 'Save'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
