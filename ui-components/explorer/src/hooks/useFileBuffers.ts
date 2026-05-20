import { useCallback, useEffect, useRef, useState } from 'react';
import type { FileMap } from '../types.js';

interface UseFileBuffersOptions {
  /** Controlled file map (saved content). */
  files?: FileMap;
  /** Initial file map for uncontrolled mode. */
  defaultFiles?: FileMap;
}

interface UseFileBuffersReturn {
  /** Saved content, keyed by path. Either `files` prop or internal uncontrolled state. */
  savedMap: FileMap;
  /** Update internal saved state (uncontrolled mode only). */
  setSavedMap: (updater: (prev: FileMap) => FileMap) => void;
  /** True if component is in controlled mode (files prop is set). */
  isControlled: boolean;
  /** Get buffer value for a path, falling back to saved content. */
  getBuffer: (path: string) => string | undefined;
  /** Get all buffers merged with saved (buffer wins on conflict). */
  getAllBuffers: () => FileMap;
  /** True if buffer exists and differs from saved. */
  isDirty: (path: string) => boolean;
  /** Write to buffer. */
  updateBuffer: (path: string, md: string) => void;
  /** Remove buffer entry (revert to saved). */
  resetBuffer: (path: string) => void;
  /** Rename a buffer (and saved, if uncontrolled). */
  renameBuffer: (oldPath: string, newPath: string) => void;
  /** Delete a buffer (and saved, if uncontrolled). */
  deleteBuffer: (path: string) => void;
  /** Bumps when buffer map mutates, so consumers can re-render. */
  bufferVersion: number;
}

export function useFileBuffers(options: UseFileBuffersOptions): UseFileBuffersReturn {
  const { files, defaultFiles } = options;
  const isControlled = files !== undefined;

  const [internalFiles, setInternalFiles] = useState<FileMap>(() => ({ ...(defaultFiles ?? {}) }));
  const savedMap = isControlled ? files : internalFiles;

  // Buffers stored in a ref so mutations do not trigger unrelated renders.
  // We bump `bufferVersion` when a re-render is needed.
  const buffersRef = useRef<Map<string, string>>(new Map());
  const [bufferVersion, setBufferVersion] = useState(0);
  const bump = useCallback(() => setBufferVersion((v) => v + 1), []);

  // When savedMap changes, prune buffers that either match saved (clean) or
  // reference paths that no longer exist.
  const savedKeysSignature = Object.keys(savedMap).sort().join('\n');
  useEffect(() => {
    let mutated = false;
    const buffers = buffersRef.current;
    for (const [path, val] of Array.from(buffers.entries())) {
      const saved = savedMap[path];
      if (saved === undefined) {
        buffers.delete(path);
        mutated = true;
      } else if (val === saved) {
        buffers.delete(path);
        mutated = true;
      }
    }
    if (mutated) bump();
    // Intentionally scoped: we want to re-run only when the *shape* of savedMap changes.
  }, [savedKeysSignature, savedMap, bump]);

  const getBuffer = useCallback(
    (path: string): string | undefined => {
      const b = buffersRef.current.get(path);
      if (b !== undefined) return b;
      return savedMap[path];
    },
    [savedMap],
  );

  const getAllBuffers = useCallback((): FileMap => {
    const merged: FileMap = { ...savedMap };
    for (const [path, val] of buffersRef.current.entries()) {
      merged[path] = val;
    }
    return merged;
  }, [savedMap]);

  const isDirty = useCallback(
    (path: string): boolean => {
      const b = buffersRef.current.get(path);
      if (b === undefined) return false;
      return b !== savedMap[path];
    },
    [savedMap],
  );

  const updateBuffer = useCallback(
    (path: string, md: string) => {
      const saved = savedMap[path];
      if (md === saved) {
        if (buffersRef.current.delete(path)) bump();
      } else {
        buffersRef.current.set(path, md);
        bump();
      }
    },
    [savedMap, bump],
  );

  const resetBuffer = useCallback(
    (path: string) => {
      if (buffersRef.current.delete(path)) bump();
    },
    [bump],
  );

  const renameBuffer = useCallback(
    (oldPath: string, newPath: string) => {
      const buffers = buffersRef.current;
      if (buffers.has(oldPath)) {
        const v = buffers.get(oldPath)!;
        buffers.delete(oldPath);
        buffers.set(newPath, v);
      }
      if (!isControlled) {
        setInternalFiles((prev) => {
          if (!(oldPath in prev)) return prev;
          const { [oldPath]: v, ...rest } = prev;
          return { ...rest, [newPath]: v };
        });
      }
      bump();
    },
    [isControlled, bump],
  );

  const deleteBuffer = useCallback(
    (path: string) => {
      buffersRef.current.delete(path);
      if (!isControlled) {
        setInternalFiles((prev) => {
          if (!(path in prev)) return prev;
          const { [path]: _removed, ...rest } = prev;
          return rest;
        });
      }
      bump();
    },
    [isControlled, bump],
  );

  const setSavedMap = useCallback(
    (updater: (prev: FileMap) => FileMap) => {
      if (isControlled) return;
      setInternalFiles(updater);
    },
    [isControlled],
  );

  return {
    savedMap,
    setSavedMap,
    isControlled,
    getBuffer,
    getAllBuffers,
    isDirty,
    updateBuffer,
    resetBuffer,
    renameBuffer,
    deleteBuffer,
    bufferVersion,
  };
}
