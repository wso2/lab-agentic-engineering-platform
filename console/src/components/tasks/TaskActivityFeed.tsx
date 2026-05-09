import { memo, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Box, Stack, Typography } from '@wso2/oxygen-ui';
import {
  Circle,
  CircleCheck,
  CircleX,
  FileText,
  GitBranch,
  GitCommit,
  Github,
  Hammer,
  Wrench,
  Workflow,
} from '@wso2/oxygen-ui-icons-react';
import type { TaskProgressEvent } from '../../services/api';

interface Props {
  agentLines: TaskProgressEvent[];
  buildLines: TaskProgressEvent[];
  agentFinal?: boolean;
  buildFinal?: boolean;
  emptyMessage?: string;
}

const ICON_SIZE = 14;

function iconFor(ev: TaskProgressEvent): ReactNode {
  switch (ev.kind) {
    case 'phase':      return <Workflow size={ICON_SIZE} />;
    case 'tool_use':   return <Wrench size={ICON_SIZE} />;
    case 'git_commit': return <GitCommit size={ICON_SIZE} />;
    case 'git_push':   return <GitBranch size={ICON_SIZE} />;
    case 'gh_action':  return <Github size={ICON_SIZE} />;
    case 'result':     return ev.status === 'failure'
      ? <CircleX size={ICON_SIZE} />
      : <CircleCheck size={ICON_SIZE} />;
    case 'build_step': return <Hammer size={ICON_SIZE} />;
    case 'log':        return <FileText size={ICON_SIZE} />;
    default:           return <Circle size={ICON_SIZE} />;
  }
}

function summaryFor(ev: TaskProgressEvent): string {
  switch (ev.kind) {
    case 'phase':      return ev.phase ?? '';
    case 'tool_use':   return `${ev.tool ?? 'tool'}${ev.summary ? ' · ' + ev.summary : ''}`;
    case 'git_commit': return `Committed${ev.summary ? ': ' + ev.summary : ''}${ev.sha ? ' (' + ev.sha.slice(0, 7) + ')' : ''}`;
    case 'git_push':   return `Pushed${ev.branch ? ' to ' + ev.branch : ''}${ev.sha ? ' (' + ev.sha.slice(0, 7) + ')' : ''}`;
    case 'gh_action':  return ev.command ?? 'gh';
    case 'result':     return ev.summary ?? (ev.status === 'success' ? 'Done' : `Failed${ev.error ? ': ' + ev.error : ''}`);
    case 'build_step': return `${ev.step ?? 'step'}${ev.phase ? ' · ' + ev.phase : ''}${ev.message ? ' · ' + ev.message : ''}`;
    case 'log':        return ev.summary ?? '';
    default:           return ev.summary ?? '';
  }
}

function formatTs(ts: string | undefined): string {
  if (!ts) return '—:—:—';
  const d = new Date(ts);
  if (isNaN(d.getTime())) return '—:—:—';
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
}

function compareEvents(a: TaskProgressEvent, b: TaskProgressEvent): number {
  if (a.ts === b.ts) return a.seq - b.seq;
  return a.ts < b.ts ? -1 : 1;
}

// Hoisted row styles — emotion hashes these once instead of re-processing
// the sx object on every row on every render.
const ROW_SX = {
  px: 1,
  py: 0.5,
  borderRadius: 1,
  '&:hover': { bgcolor: 'action.hover' },
} as const;

const TS_SX = {
  fontFamily: 'monospace',
  fontSize: '0.72rem',
  color: 'text.disabled',
  minWidth: 64,
  flexShrink: 0,
} as const;

const ICON_BOX_SX_DEFAULT = {
  flexShrink: 0,
  display: 'inline-flex',
  alignItems: 'center',
  color: 'text.secondary',
} as const;

const ICON_BOX_SX_FAILURE = {
  ...ICON_BOX_SX_DEFAULT,
  color: 'error.main',
} as const;

const SUMMARY_SX_DEFAULT = {
  fontSize: '0.8rem',
  color: 'text.primary',
  wordBreak: 'break-word',
} as const;

const SUMMARY_SX_FAILURE = {
  ...SUMMARY_SX_DEFAULT,
  color: 'error.main',
} as const;

interface RowProps {
  ev: TaskProgressEvent;
}

const ActivityRow = memo(function ActivityRow({ ev }: RowProps) {
  const isFailure = ev.kind === 'result' && ev.status === 'failure';
  return (
    <Stack direction="row" spacing={1.5} alignItems="flex-start" sx={ROW_SX}>
      <Typography component="span" sx={TS_SX}>{formatTs(ev.ts)}</Typography>
      <Box component="span" sx={isFailure ? ICON_BOX_SX_FAILURE : ICON_BOX_SX_DEFAULT}>
        {iconFor(ev)}
      </Box>
      <Typography component="span" sx={isFailure ? SUMMARY_SX_FAILURE : SUMMARY_SX_DEFAULT}>
        {summaryFor(ev)}
      </Typography>
    </Stack>
  );
});

// mergeSorted does a 2-pointer merge of two already-sorted arrays. The
// inputs from the BFF are sorted by (ts, seq); previously we concatenated
// and re-sorted on every poll (O(N log N)). The merge is O(N) and the
// dedup is fold-into-the-walk.
function mergeSorted(a: TaskProgressEvent[], b: TaskProgressEvent[]): TaskProgressEvent[] {
  const out: TaskProgressEvent[] = [];
  out.length = a.length + b.length;
  let i = 0, j = 0, k = 0;
  let lastKey = '';
  const writeIfNew = (ev: TaskProgressEvent) => {
    const key = `${ev.ts}|${ev.seq}|${ev.kind}`;
    if (key === lastKey) return;
    lastKey = key;
    out[k++] = ev;
  };
  while (i < a.length && j < b.length) {
    if (compareEvents(a[i], b[j]) <= 0) writeIfNew(a[i++]);
    else writeIfNew(b[j++]);
  }
  while (i < a.length) writeIfNew(a[i++]);
  while (j < b.length) writeIfNew(b[j++]);
  out.length = k;
  return out;
}

export function TaskActivityFeed({ agentLines, buildLines, agentFinal, buildFinal, emptyMessage }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null);
  // Stick-to-bottom: when the user is near the bottom, auto-scroll on new
  // events; if they scroll up to read history, we stop auto-scrolling and
  // surface a "jump to latest" affordance.
  const [stickToBottom, setStickToBottom] = useState(true);

  const merged = useMemo(() => mergeSorted(agentLines, buildLines), [agentLines, buildLines]);

  useEffect(() => {
    if (!stickToBottom) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [merged.length, stickToBottom]);

  const handleScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    setStickToBottom(nearBottom);
  };

  if (merged.length === 0) {
    return (
      <Box sx={{ py: 4, textAlign: 'center' }}>
        <Typography variant="body2" color="text.disabled">
          {emptyMessage ?? 'Waiting for activity…'}
        </Typography>
      </Box>
    );
  }

  return (
    <Box
      ref={scrollRef}
      onScroll={handleScroll}
      sx={{ position: 'relative', py: 1, maxHeight: 480, overflowY: 'auto' }}
    >
      <Stack spacing={0.5}>
        {merged.map((ev, i) => (
          <ActivityRow key={`${ev.ts}-${ev.seq}-${i}`} ev={ev} />
        ))}
        {(agentFinal || buildFinal) && (
          <Box sx={{ px: 1, py: 1, textAlign: 'center' }}>
            <Typography variant="caption" color="text.disabled">— end of feed —</Typography>
          </Box>
        )}
      </Stack>
      {!stickToBottom && (
        <Box
          onClick={() => {
            setStickToBottom(true);
            const el = scrollRef.current;
            if (el) el.scrollTop = el.scrollHeight;
          }}
          sx={{
            position: 'sticky',
            bottom: 4,
            mx: 'auto',
            width: 'fit-content',
            px: 1.5, py: 0.5,
            borderRadius: 4,
            bgcolor: 'primary.main',
            color: 'primary.contrastText',
            cursor: 'pointer',
            boxShadow: 2,
            fontSize: '0.72rem',
            fontWeight: 600,
            '&:hover': { bgcolor: 'primary.dark' },
          }}
        >
          ↓ Jump to latest
        </Box>
      )}
    </Box>
  );
}
