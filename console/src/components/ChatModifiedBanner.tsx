/**
 * Banner shown above the requirements editor when the active file has
 * been modified by the chat agent during the current session.
 *
 * The banner pins two actions:
 *  - Accept: clears the file from the modified set, leaving the
 *    current on-disk content as-is. When the last modified file is
 *    accepted the page drops the baseline snapshot via the BFF.
 *  - Revert: rewrites the file to its baseline content (or deletes it
 *    if it was created post-baseline). Held under the requirements dir
 *    lock so it serialises with chat / manual edits.
 *
 * The banner stays out of the editor's keyboard / mouse surface — it's
 * a passive strip that does not block typing or selection. The diff
 * itself is rendered inline (via `MdDiffViewer`) in place of the
 * editor, so a separate "View original" affordance is no longer needed.
 */

import { useState } from 'react';
import {
  Box,
  Button,
  CircularProgress,
  Stack,
  Tooltip,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import { Check, RotateCcw, Sparkles } from '@wso2/oxygen-ui-icons-react';

export interface ChatModifiedBannerProps {
  filename: string;
  /** Disabled while a chat turn is in flight against the same dir. */
  busy?: boolean;
  /** Set true while Revert or Accept is in flight to prevent double-clicks. */
  pending?: boolean;
  /** Line-diff counts surfaced as a "+N / -M" chip next to the title. */
  counts?: { added: number; removed: number };
  onAccept: () => void;
  onRevert: () => void;
}

export default function ChatModifiedBanner({
  filename,
  busy,
  pending,
  counts,
  onAccept,
  onRevert,
}: ChatModifiedBannerProps) {
  const theme = useTheme();
  const [hoverRevert, setHoverRevert] = useState(false);

  const accent =
    (theme.vars?.palette.primary?.main as string | undefined) ??
    theme.palette?.primary?.main ??
    '#6366f1';
  const bg =
    theme.palette?.mode === 'dark'
      ? 'rgba(99, 102, 241, 0.14)'
      : 'rgba(99, 102, 241, 0.08)';
  const borderColor =
    theme.palette?.mode === 'dark'
      ? 'rgba(99, 102, 241, 0.45)'
      : 'rgba(99, 102, 241, 0.32)';

  const actionsDisabled = busy || pending;
  const hasCounts = !!counts && (counts.added > 0 || counts.removed > 0);

  return (
    <Box
      data-testid="chat-modified-banner"
      data-filename={filename}
      sx={{
        display: 'flex',
        alignItems: 'center',
        gap: 1.5,
        px: 2,
        py: 1,
        bgcolor: bg,
        borderTop: '1px solid',
        borderBottom: '1px solid',
        borderColor,
        flexShrink: 0,
      }}
    >
      <Stack direction="row" alignItems="center" gap={1} sx={{ flexGrow: 1, minWidth: 0 }}>
        <Box
          sx={{
            width: 22,
            height: 22,
            borderRadius: '50%',
            bgcolor: accent,
            color: '#fff',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            flexShrink: 0,
          }}
        >
          <Sparkles size={12} />
        </Box>
        <Typography variant="body2" sx={{ fontWeight: 600, fontSize: '0.8125rem', color: accent }}>
          Modified by chat
        </Typography>
        {hasCounts && (
          <Stack
            direction="row"
            alignItems="center"
            spacing={0.5}
            data-testid="chat-modified-banner-counts"
            sx={{
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontSize: '0.75rem',
              fontWeight: 600,
              lineHeight: 1,
            }}
          >
            {counts!.added > 0 && (
              <Box component="span" sx={{ color: '#16a34a' }}>{`+${counts!.added}`}</Box>
            )}
            {counts!.removed > 0 && (
              <Box component="span" sx={{ color: '#dc2626' }}>{`-${counts!.removed}`}</Box>
            )}
          </Stack>
        )}
        <Typography
          variant="body2"
          sx={{
            fontSize: '0.8125rem',
            color: 'text.secondary',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          Pending review · the diff below shows what changed since the session started.
        </Typography>
      </Stack>

      <Stack direction="row" alignItems="center" gap={0.75}>
        <Tooltip
          title={
            hoverRevert
              ? 'Replace this file with its baseline content. Any manual edits since then will be lost.'
              : 'Revert this file to baseline'
          }
        >
          <span>
            <Button
              size="small"
              variant="outlined"
              color="warning"
              startIcon={
                pending ? <CircularProgress size={12} color="inherit" /> : <RotateCcw size={14} />
              }
              onClick={onRevert}
              disabled={actionsDisabled}
              onMouseEnter={() => setHoverRevert(true)}
              onMouseLeave={() => setHoverRevert(false)}
              data-testid="chat-modified-revert"
              sx={{ minWidth: 'auto', textTransform: 'none', fontSize: '0.75rem' }}
            >
              Revert
            </Button>
          </span>
        </Tooltip>
        <Tooltip title="Keep the chat changes and clear the banner">
          <span>
            <Button
              size="small"
              variant="contained"
              color="primary"
              startIcon={<Check size={14} />}
              onClick={onAccept}
              disabled={actionsDisabled}
              data-testid="chat-modified-accept"
              sx={{ minWidth: 'auto', textTransform: 'none', fontSize: '0.75rem' }}
            >
              Accept
            </Button>
          </span>
        </Tooltip>
      </Stack>
    </Box>
  );
}
