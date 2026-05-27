import { useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Button,
  Divider,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { Info } from '@wso2/oxygen-ui-icons-react';
import { orgSkillsApi, type SkillDetail } from '../../services/api/orgSkills';
import { kindChipColor, kindLabel } from './skillKind';

interface SkillViewerProps {
  orgHandle: string;
  name: string | null;
  open: boolean;
  onClose: () => void;
}

/**
 * SkillViewer — read-only dialog showing a skill's frontmatter summary,
 * SKILL.md body (raw markdown, monospace), and any reference files.
 * Built-ins surface a "read-only in v1" hint pointing at custom skills.
 */
export default function SkillViewer({ orgHandle, name, open, onClose }: SkillViewerProps) {
  const [detail, setDetail] = useState<SkillDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open || !name) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    setDetail(null);
    orgSkillsApi
      .get(orgHandle, name)
      .then((d) => {
        if (!cancelled) setDetail(d);
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, name, orgHandle]);

  return (
    <Dialog
      open={open}
      onClose={onClose}
      maxWidth="md"
      fullWidth
      slotProps={{ paper: { sx: { backgroundColor: 'background.default', backgroundImage: 'none', opacity: 1, backdropFilter: 'none' } } }}
    >
      <DialogTitle>
        <Stack direction="row" alignItems="center" gap={1.5}>
          <Typography variant="h6" sx={{ fontWeight: 700 }}>
            {name}
          </Typography>
          {detail && (
            <>
              <Chip size="small" color={kindChipColor(detail.kind)} label={kindLabel(detail.kind)} />
              <Chip size="small" variant="outlined" label={`v${detail.version}`} />
              {!detail.editable && <Chip size="small" variant="outlined" label="read-only" />}
            </>
          )}
        </Stack>
      </DialogTitle>
      <DialogContent dividers>
        {loading && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress size={28} />
          </Box>
        )}
        {error && <Alert severity="error">{error}</Alert>}
        {detail && (
          <Stack gap={2}>
            {detail.kind === 'builtin' && (
              <Alert severity="info" icon={<Info size={18} />}>
                This skill ships with the platform and is read-only in v1. To layer
                org-specific rules on top, create a Custom skill and attach it alongside
                this one on your project.
              </Alert>
            )}
            <Box>
              <Typography variant="overline" color="text.secondary">
                Description
              </Typography>
              <Typography variant="body2">{detail.description}</Typography>
            </Box>
            {(detail.license || detail.compatibility) && (
              <Stack direction="row" gap={3}>
                {detail.license && (
                  <Typography variant="caption" color="text.secondary">
                    License: {detail.license}
                  </Typography>
                )}
                {detail.compatibility && (
                  <Typography variant="caption" color="text.secondary">
                    Compatibility: {detail.compatibility}
                  </Typography>
                )}
              </Stack>
            )}
            <Divider />
            <Box>
              <Typography variant="overline" color="text.secondary">
                SKILL.md
              </Typography>
              <Box
                component="pre"
                sx={{
                  mt: 0.5,
                  p: 2,
                  borderRadius: 1,
                  bgcolor: 'action.hover',
                  fontFamily: 'monospace',
                  fontSize: 13,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                  maxHeight: 480,
                  overflow: 'auto',
                }}
              >
                {detail.skillMd}
              </Box>
            </Box>
            {Object.keys(detail.references ?? {}).length > 0 && (
              <Box>
                <Typography variant="overline" color="text.secondary">
                  References
                </Typography>
                <Stack gap={0.5}>
                  {Object.keys(detail.references).map((k) => (
                    <Typography key={k} variant="caption" sx={{ fontFamily: 'monospace' }}>
                      {k}
                    </Typography>
                  ))}
                </Stack>
              </Box>
            )}
          </Stack>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
}
