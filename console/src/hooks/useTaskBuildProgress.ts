import { api } from '../services/api';
import type { TaskStatus } from '../services/api';
import { useCursorPolling } from './useCursorPolling';

// useTaskBuildProgress mirrors useTaskAgentProgress but reads from
// /progress/build, which surfaces synthetic build_step lines derived
// from WorkflowRun.Status.Tasks[]. Active during `building`; stops on
// final:true.
export function useTaskBuildProgress(
  orgId: string | undefined,
  projectId: string | undefined,
  taskId: string | undefined,
  taskStatus: TaskStatus | undefined,
) {
  // Fetch once for any task past merged so the build_step feed shows
  // historical builds; only poll while the task is actively building.
  const enabled = !!orgId && !!projectId && !!taskId
    && (taskStatus === 'merged' || taskStatus === 'building'
        || taskStatus === 'deployed' || taskStatus === 'failed');
  const isLive = taskStatus === 'building' || taskStatus === 'merged';

  const { lines, final, isLoading, error } = useCursorPolling({
    queryKey: ['taskBuildProgress', orgId, projectId, taskId],
    fetcher: (cursor) => api.getTaskBuildProgress(orgId!, projectId!, taskId!, cursor),
    enabled,
    isLive,
    taskIdentity: taskId,
  });

  return { lines, final, isLoading, error };
}
