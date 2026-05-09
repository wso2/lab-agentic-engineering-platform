import { useEffect, useRef, useState } from 'react';
import { useQuery, type QueryKey } from '@tanstack/react-query';
import type { TaskProgressEvent, TaskProgressResponse } from '../services/api';

const POLL_MS = 3_000;

interface CursorPollingOptions {
  // queryKey identifies the logical query — taskId, etc. Cursor is
  // intentionally NOT part of the key (it advances on every poll, which
  // would mint a fresh cache entry per poll and leak memory across long
  // runs).
  queryKey: QueryKey;
  // fetcher is called with the current cursor on each poll.
  fetcher: (cursor: number) => Promise<TaskProgressResponse>;
  // enabled gates the query entirely — pass false to suspend until a
  // taskId / status is available.
  enabled: boolean;
  // isLive controls polling cadence — only poll while the task is in
  // its live phase. Once isLive flips false (or final:true returns)
  // the query goes dormant.
  isLive: boolean;
  // taskIdentity resets the accumulator + cursor when it changes (e.g.
  // navigation to a different task).
  taskIdentity: string | undefined;
  // trackPhase opts into surfacing the latest `phase` event back to
  // the caller. Build progress doesn't need this; agent progress does.
  trackPhase?: boolean;
}

interface CursorPollingResult {
  lines: TaskProgressEvent[];
  phase: string | undefined;
  final: boolean;
  isLoading: boolean;
  error: Error | null;
}

// useCursorPolling is the shared cursor + accumulator + final-flag
// scaffolding behind useTaskAgentProgress and useTaskBuildProgress. Both
// flows are NDJSON deltas keyed on `cursorMillis`; the only differences
// are the API endpoint, the live-phase predicate, and whether `phase`
// is tracked.
//
// The cursor lives in a ref so each refetch (driven by React Query's
// `refetchInterval`) fires `fetcher(cursorRef.current)` with the latest
// position. Crucially, the cursor is NOT part of the queryKey — that
// would mint a fresh cache entry per poll and leak memory.
export function useCursorPolling(opts: CursorPollingOptions): CursorPollingResult {
  const cursorRef = useRef(0);
  const [accumulated, setAccumulated] = useState<TaskProgressEvent[]>([]);
  const [phase, setPhase] = useState<string | undefined>(undefined);
  const [final, setFinal] = useState(false);
  const seenTask = useRef<string | undefined>(undefined);

  useEffect(() => {
    if (seenTask.current !== opts.taskIdentity) {
      seenTask.current = opts.taskIdentity;
      cursorRef.current = 0;
      setAccumulated([]);
      setPhase(undefined);
      setFinal(false);
    }
  }, [opts.taskIdentity]);

  const query = useQuery<TaskProgressResponse>({
    queryKey: opts.queryKey,
    queryFn: () => opts.fetcher(cursorRef.current),
    enabled: opts.enabled,
    refetchInterval: opts.enabled && opts.isLive && !final ? POLL_MS : false,
  });

  useEffect(() => {
    const data = query.data;
    if (!data) return;
    if (data.lines && data.lines.length > 0) {
      setAccumulated((prev) => [...prev, ...data.lines]);
    }
    if (data.cursorMillis > cursorRef.current) {
      cursorRef.current = data.cursorMillis;
    }
    if (opts.trackPhase && data.phase && data.phase !== phase) {
      setPhase(data.phase);
    }
    if (data.final && !final) setFinal(true);
  }, [query.data]); // eslint-disable-line react-hooks/exhaustive-deps

  return {
    lines: accumulated,
    phase,
    final,
    isLoading: query.isLoading,
    error: query.error as Error | null,
  };
}
