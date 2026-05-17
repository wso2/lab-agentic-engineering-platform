/**
 * Side-by-side dialog comparing a requirements file's baseline content
 * (captured at the start of the chat session) against its current
 * working-tree content. Driven by the "View original" action on
 * {@link ChatModifiedBanner}.
 *
 * The baseline pane renders read-only markdown so links, lists, and
 * tables look the same as the editor; the current pane reuses the same
 * renderer for an apples-to-apples comparison.
 *
 * Tombstone case: if `existed=false` (the file was created by chat post-
 * baseline) the baseline pane shows an explanatory tombstone instead.
 */

import {
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { MdEditor } from '@asdlc/md-editor';

export interface BaselineContentDialogProps {
  open: boolean;
  filename: string;
  /**
   * - `loading`: still fetching baseline content.
   * - `present`: file existed at baseline; show content.
   * - `tombstone`: file did not exist at baseline (created by chat).
   * - `missing`: snapshot itself is missing (rare — e.g. server cleanup).
   */
  state: 'loading' | 'present' | 'tombstone' | 'missing';
  baseline?: string;
  current: string;
  onClose: () => void;
}

export default function BaselineContentDialog({
  open,
  filename,
  state,
  baseline,
  current,
  onClose,
}: BaselineContentDialogProps) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      maxWidth="lg"
      fullWidth
      data-testid="baseline-content-dialog"
    >
      <DialogTitle sx={{ pr: 6 }}>
        <Stack direction="row" alignItems="center" gap={1.5} flexWrap="wrap">
          <Typography variant="h6">Original vs Current</Typography>
          <Chip label={filename} size="small" />
        </Stack>
        <Typography variant="caption" color="text.secondary">
          Side-by-side comparison. Left: content captured at the start of the chat session.
          Right: current working-tree content.
        </Typography>
      </DialogTitle>
      <DialogContent dividers sx={{ p: 0, height: '70vh', display: 'flex', flexDirection: 'column', minHeight: 400 }}>
        <Box sx={{ flex: 1, minHeight: 0, display: 'flex' }}>
          <Pane label="Original (baseline)" tone="baseline">
            <BaselinePane state={state} baseline={baseline} />
          </Pane>
          <Box sx={{ width: '1px', bgcolor: 'divider' }} />
          <Pane label="Current (working tree)" tone="current">
            <ReadOnlyMd value={current} />
          </Pane>
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} variant="contained" data-testid="baseline-content-dialog-close">
          Close
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function Pane({
  label,
  tone,
  children,
}: {
  label: string;
  tone: 'baseline' | 'current';
  children: React.ReactNode;
}) {
  const accent = tone === 'baseline' ? 'rgba(99, 102, 241, 0.12)' : 'rgba(46, 125, 50, 0.10)';
  const labelColor = tone === 'baseline' ? '#6366f1' : '#2e7d32';
  return (
    <Box sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
      <Box
        sx={{
          px: 1.5,
          py: 0.75,
          bgcolor: accent,
          borderBottom: '1px solid',
          borderColor: 'divider',
          flexShrink: 0,
        }}
      >
        <Typography variant="caption" sx={{ fontWeight: 700, fontSize: '0.7rem', color: labelColor, textTransform: 'uppercase', letterSpacing: 0.5 }}>
          {label}
        </Typography>
      </Box>
      <Box sx={{ flex: 1, minHeight: 0, overflow: 'auto' }}>{children}</Box>
    </Box>
  );
}

function BaselinePane({ state, baseline }: { state: BaselineContentDialogProps['state']; baseline?: string }) {
  if (state === 'loading') {
    return (
      <Stack alignItems="center" justifyContent="center" sx={{ height: '100%', py: 6, gap: 2 }}>
        <CircularProgress size={32} />
        <Typography variant="body2" color="text.secondary">
          Loading baseline…
        </Typography>
      </Stack>
    );
  }
  if (state === 'tombstone') {
    return (
      <Stack alignItems="center" justifyContent="center" sx={{ height: '100%', py: 6, px: 3, gap: 1, textAlign: 'center' }}>
        <Typography variant="body2" fontWeight={600}>
          This file did not exist at baseline.
        </Typography>
        <Typography variant="caption" color="text.secondary">
          The chat agent created it during this session. Reverting will delete the working-tree file.
        </Typography>
      </Stack>
    );
  }
  if (state === 'missing') {
    return (
      <Stack alignItems="center" justifyContent="center" sx={{ height: '100%', py: 6, px: 3, gap: 1, textAlign: 'center' }}>
        <Typography variant="body2" fontWeight={600}>
          Baseline snapshot is no longer available.
        </Typography>
        <Typography variant="caption" color="text.secondary">
          The server may have cleaned it up; Accept the file from the banner to clear the indicator.
        </Typography>
      </Stack>
    );
  }
  return <ReadOnlyMd value={baseline ?? ''} />;
}

function ReadOnlyMd({ value }: { value: string }) {
  return (
    <Box sx={{ height: '100%', '& .ProseMirror': { p: 2 } }}>
      <MdEditor value={value} onChange={() => {}} readOnly fillHeight contentMaxWidth="none" showToolbar={false} />
    </Box>
  );
}
