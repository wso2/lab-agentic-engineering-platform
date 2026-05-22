import { api } from '../services/api';
import type { TaskStatus } from '../services/api';
import { useCursorPolling } from './useCursorPolling';

// useTaskAgentProgress streams the coding-agent's NDJSON output from
// the BFF's /progress/agent endpoint, accumulating lines locally and
// echoing the cursor on each poll. Polling stops once the response
// returns final:true OR the task moves past `in_progress`.
export function useTaskAgentProgress(
  orgId: string | undefined,
  projectId: string | undefined,
  taskId: string | undefined,
  taskStatus: TaskStatus | undefined,
) {
  // Fetch the agent feed for any task that ever ran (past `pending`),
  // not just in-flight ones — the design intends `final:true` to freeze
  // a populated historical feed. We always do at least one fetch; only
  // polling is gated on the active phase.
  const enabled = !!orgId && !!projectId && !!taskId
    && taskStatus !== undefined
    && taskStatus !== 'pending'
    && taskStatus !== 'on_hold';
  const isLive = taskStatus === 'in_progress' || taskStatus === 'testing';

  const { lines, phase, final, isLoading, error } = useCursorPolling({
    queryKey: ['taskAgentProgress', orgId, projectId, taskId],
    fetcher: (cursor) => api.getTaskAgentProgress(orgId!, projectId!, taskId!, cursor),
    enabled,
    isLive,
    taskIdentity: taskId,
    trackPhase: true,
  });

  return { lines, phase, final, isLoading, error };
}
