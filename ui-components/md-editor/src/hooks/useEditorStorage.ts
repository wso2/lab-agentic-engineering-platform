import { useState, useCallback, useRef, useEffect } from 'react';
import type { UseEditorStorageOptions, UseEditorStorageReturn } from '../types.js';

interface StorageData {
  snapshots: string[];
  pointer: number;
}

function loadFromStorage(key: string): StorageData {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return { snapshots: [], pointer: -1 };
    const data = JSON.parse(raw) as StorageData;
    if (!Array.isArray(data.snapshots) || typeof data.pointer !== 'number') {
      return { snapshots: [], pointer: -1 };
    }
    // Clamp pointer to valid range
    const pointer = Math.min(Math.max(data.pointer, 0), data.snapshots.length - 1);
    return { snapshots: data.snapshots, pointer };
  } catch {
    return { snapshots: [], pointer: -1 };
  }
}

function saveToStorage(key: string, data: StorageData): void {
  try {
    localStorage.setItem(key, JSON.stringify(data));
  } catch {
    // localStorage full or unavailable — silently ignore
  }
}

export function useEditorStorage(options: UseEditorStorageOptions): UseEditorStorageReturn {
  const { storageKey, maxSnapshots = 50, debounceMs = 500 } = options;

  // Load initial state from localStorage
  const [state, setState] = useState<StorageData>(() => loadFromStorage(storageKey));
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const stateRef = useRef(state);
  stateRef.current = state;

  // Persist to localStorage (debounced)
  const persistDebounced = useCallback(
    (data: StorageData) => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => {
        saveToStorage(storageKey, data);
      }, debounceMs);
    },
    [storageKey, debounceMs],
  );

  // Cleanup debounce timer and flush on unmount
  useEffect(() => {
    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
        // Flush final state to storage
        saveToStorage(storageKey, stateRef.current);
      }
    };
  }, [storageKey]);

  const onChange = useCallback(
    (markdown: string) => {
      setState((prev) => {
        // Skip if identical to current snapshot
        if (prev.pointer >= 0 && prev.snapshots[prev.pointer] === markdown) {
          return prev;
        }

        // Truncate any redo history
        const base = prev.snapshots.slice(0, prev.pointer + 1);
        base.push(markdown);

        // Cap at maxSnapshots
        const trimmed = base.length > maxSnapshots ? base.slice(base.length - maxSnapshots) : base;
        const newState: StorageData = {
          snapshots: trimmed,
          pointer: trimmed.length - 1,
        };

        persistDebounced(newState);
        return newState;
      });
    },
    [maxSnapshots, persistDebounced],
  );

  const undo = useCallback(() => {
    setState((prev) => {
      if (prev.pointer <= 0) return prev;
      const newState: StorageData = { ...prev, pointer: prev.pointer - 1 };
      persistDebounced(newState);
      return newState;
    });
  }, [persistDebounced]);

  const redo = useCallback(() => {
    setState((prev) => {
      if (prev.pointer >= prev.snapshots.length - 1) return prev;
      const newState: StorageData = { ...prev, pointer: prev.pointer + 1 };
      persistDebounced(newState);
      return newState;
    });
  }, [persistDebounced]);

  const clear = useCallback(() => {
    setState({ snapshots: [], pointer: -1 });
    try {
      localStorage.removeItem(storageKey);
    } catch {
      // ignore
    }
  }, [storageKey]);

  const value = state.pointer >= 0 ? state.snapshots[state.pointer] : '';
  const canUndo = state.pointer > 0;
  const canRedo = state.pointer < state.snapshots.length - 1;

  return { value, onChange, undo, redo, canUndo, canRedo, clear };
}
