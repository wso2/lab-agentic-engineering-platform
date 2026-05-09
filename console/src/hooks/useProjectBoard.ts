import { useCallback, useEffect, useState } from 'react';
import { api, ApiError } from '../services/api';
import type { ProjectBoard } from '../services/api';
import { projectRequirementsPath, projectArchitecturePath } from '../lib/paths';

const EMPTY_BOARD: ProjectBoard = { todo: [], inProgress: [], done: [], onHold: [], failed: [], url: '' };

export type GenerateBannerVariant = 'info' | 'warning' | 'error' | 'success';

export interface GenerateBanner {
  variant: GenerateBannerVariant;
  message: string;
  action?: { label: string; path: string };
  autoDismiss: boolean;
}

export function useProjectBoard(orgId: string | undefined, projectId: string | undefined) {
  const [board, setBoard] = useState<ProjectBoard>(EMPTY_BOARD);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isGenerating, setIsGenerating] = useState(false);
  const [isDispatching, setIsDispatching] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [generateBanner, setGenerateBanner] = useState<GenerateBanner | null>(null);
  const [hideGenerateButton, setHideGenerateButton] = useState(false);

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
    setGenerateBanner(null);
    setHideGenerateButton(false);
    setIsGenerating(true);

    let firstResponseHandled = false;

    try {
      await api.streamGenerateTasks(orgId, projectId, {
        onPlanItem: () => {
          if (!firstResponseHandled) {
            firstResponseHandled = true;
            setHideGenerateButton(true);
            setGenerateBanner({ variant: 'info', message: 'Tasks are being generated…', autoDismiss: false });
          }
        },
        onFinish: (_e) => {
          if (!firstResponseHandled) {
            firstResponseHandled = true;
            setHideGenerateButton(true);
            setGenerateBanner({
              variant: 'warning',
              message: 'Tasks are already up to date and ready to execute.',
              autoDismiss: true,
            });
          } else {
            // Normal generation completed — replace the INFO banner with SUCCESS.
            setGenerateBanner({
              variant: 'success',
              message: 'Tasks generated successfully.',
              autoDismiss: true,
            });
          }
        },
        onError: (e) => {
          const errorText = e.errorText ?? '';

          if (!firstResponseHandled) {
            firstResponseHandled = true;

            if (errorText === 'spec not found') {
              setGenerateBanner({
                variant: 'error',
                message: 'No spec found for this project. Create a spec before generating tasks.',
                action: { label: 'Go to Spec', path: projectRequirementsPath(orgId, projectId) },
                autoDismiss: true,
              });
              return; // generate button stays visible
            }

            if (errorText === 'design not found') {
              setGenerateBanner({
                variant: 'error',
                message: 'No architecture design found. Complete the design before generating tasks.',
                action: { label: 'Go to Architecture', path: projectArchitecturePath(orgId, projectId) },
                autoDismiss: true,
              });
              return; // generate button stays visible
            }
          }

          // mid-stream plan-scope error (or unrecognised first-frame error)
          setHideGenerateButton(false);
          setGenerateBanner({
            variant: 'error',
            message: errorText || 'An error occurred during task generation.',
            autoDismiss: true,
          });
        },
      });
    } catch (err) {
      setHideGenerateButton(false);
      if (err instanceof ApiError && err.status === 409) {
        setActionError('Tasks are already in progress. Cannot regenerate.');
      } else {
        console.error('Failed to generate tasks:', err);
        setActionError('Failed to generate tasks.');
      }
    } finally {
      await loadBoard();
      setIsGenerating(false);
      // Clear the INFO banner once the stream ends.
      // Error and warning banners are intentionally left — they auto-dismiss via the page's useEffect.
      setGenerateBanner(prev => (prev?.variant === 'info' ? null : prev));
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
    generateBanner,
    hideGenerateButton,
    clearGenerateBanner: () => setGenerateBanner(null),
  };
}
