import { useCallback, useEffect, useState } from 'react';
import { api, ApiError } from '../services/api';
import type { ProjectBoard } from '../services/api';

const EMPTY_BOARD: ProjectBoard = { todo: [], inProgress: [], done: [], onHold: [], failed: [] };

export function useProjectBoard(orgId: string | undefined, projectId: string | undefined) {
  const [board, setBoard] = useState<ProjectBoard>(EMPTY_BOARD);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isGenerating, setIsGenerating] = useState(false);
  const [isDispatching, setIsDispatching] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  const loadBoard = useCallback(async () => {
    if (!orgId || !projectId) { setIsLoading(false); return; }
    try {
      const data = await api.getProjectBoard(orgId, projectId);
      setBoard(data);
    } catch (err) {
      console.error('Failed to load board:', err);
      setError('Failed to load tasks');
    } finally {
      setIsLoading(false);
    }
  }, [orgId, projectId]);

  useEffect(() => {
    void loadBoard();
    const interval = setInterval(() => { void loadBoard(); }, 5000);
    return () => clearInterval(interval);
  }, [loadBoard]);

  const handleGenerate = async () => {
    if (!orgId || !projectId) return;
    setActionError(null);
    setIsGenerating(true);
    try {
      await api.streamGenerateTasks(orgId, projectId, {
        onError: (e) => {
          setActionError(e.errorText || 'Failed to generate tasks.');
        },
        onFinish: () => {
          // board reloads in finally block
        },
      });
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setActionError('Tasks are already in progress. Cannot regenerate.');
      } else {
        console.error('Failed to generate tasks:', err);
        setActionError('Failed to generate tasks.');
      }
    } finally {
      await loadBoard();
      setIsGenerating(false);
    }
  };

  const handleStartImplementation = async () => {
    if (!orgId || !projectId) return;
    setActionError(null);
    setIsDispatching(true);
    try {
      await api.dispatchTasks(orgId, projectId);
      await loadBoard();
    } catch (err) {
      console.error('Failed to dispatch tasks:', err);
      setActionError('Failed to start implementation.');
      setIsDispatching(false);
    }
  };

  const totalTasks =
    board.todo.length + board.inProgress.length + board.done.length + board.onHold.length + board.failed.length;

  return {
    board,
    isLoading,
    error,
    isGenerating,
    isDispatching,
    actionError,
    totalTasks,
    handleGenerate,
    handleStartImplementation,
    clearActionError: () => setActionError(null),
  };
}
