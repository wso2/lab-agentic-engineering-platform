import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import type { TaskStatus, TaskStatusResponse } from '../services/api';

const TERMINAL: TaskStatus[] = ['deployed', 'rejected', 'failed', 'abandoned'];

const ACTIVE_INTERVAL_MS = 5_000;

// useTaskStatus polls /tasks/{id}/status at 5s while non-terminal,
// stops once the task settles. Tab-visibility gating comes from the
// QueryClient default refetchIntervalInBackground:false.
export function useTaskStatus(orgId: string | undefined, projectId: string | undefined, taskId: string | undefined) {
  return useQuery<TaskStatusResponse>({
    queryKey: ['taskStatus', orgId, projectId, taskId],
    queryFn: () => api.getTaskStatus(orgId!, projectId!, taskId!),
    enabled: !!orgId && !!projectId && !!taskId,
    refetchInterval: (q) => {
      const data = q.state.data;
      if (!data || !data.task) return ACTIVE_INTERVAL_MS;
      return TERMINAL.includes(data.task.status) ? false : ACTIVE_INTERVAL_MS;
    },
  });
}
