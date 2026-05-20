import {
  useCallback,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
} from 'react';
import { Box, Stack } from '@wso2/oxygen-ui';
import type { MdEditorRef } from '@asdlc/md-editor';
import type { FileMap, ExplorerProps, ExplorerRef } from './types.js';
import { useFileBuffers } from './hooks/useFileBuffers.js';
import { Sidebar } from './sidebar/Sidebar.js';
import { ActiveFileEditor } from './editor/ActiveFileEditor.js';
import { scrollToHeading } from './toc/scrollToHeading.js';

function generateDefaultFilename(existing: Set<string>): string {
  let n = 1;
  while (existing.has(`Untitled ${n}.md`)) n++;
  return `Untitled ${n}.md`;
}

export function Explorer({
  files,
  defaultFiles,
  onFileChange,
  activePath: activePathProp,
  defaultActivePath,
  onActivePathChange,
  onAddFile,
  addFileMenu,
  onRename,
  onDelete,
  customViews,
  pendingPaths,
  chatModifiedPaths,
  transparentFolders,
  getFolderIcon,
  showHeadings = true,
  getFileRenderer,
  getFileLabel,
  getFileSortKey,
  searchPlaceholder = 'Search documents',
  sidebarWidth = 280,
  minHeight,
  maxHeight,
  className,
  emptyState,
  editorProps,
  editorRef,
}: ExplorerProps) {
  const customViewById = useMemo(() => {
    const map = new Map<string, NonNullable<typeof customViews>[number]>();
    for (const v of customViews ?? []) map.set(v.id, v);
    return map;
  }, [customViews]);
  const buffers = useFileBuffers({ files, defaultFiles });
  const {
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
  } = buffers;

  const isActiveControlled = activePathProp !== undefined;
  const [internalActive, setInternalActive] = useState<string | null>(defaultActivePath ?? null);
  const activePath = isActiveControlled ? activePathProp : internalActive;
  const activeCustomView = activePath ? customViewById.get(activePath) : undefined;

  const setActive = useCallback(
    (path: string | null) => {
      if (!isActiveControlled) setInternalActive(path);
      onActivePathChange?.(path);
    },
    [isActiveControlled, onActivePathChange],
  );

  const paths = useMemo(
    () =>
      Object.keys(savedMap).sort((a, b) => {
        const keyA = getFileSortKey?.(a);
        const keyB = getFileSortKey?.(b);
        // Files with a host-supplied key win over alpha-only. When only
        // one side has a key, the keyed file sorts first.
        if (keyA !== undefined && keyB !== undefined && keyA !== keyB) {
          return keyA - keyB;
        }
        if (keyA !== undefined && keyB === undefined) return -1;
        if (keyA === undefined && keyB !== undefined) return 1;
        return a.localeCompare(b, undefined, { sensitivity: 'base' });
      }),
    [savedMap, getFileSortKey],
  );

  const dirtyPaths = useMemo(() => {
    const s = new Set<string>();
    for (const p of paths) {
      if (isDirty(p)) s.add(p);
    }
    return s;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bufferVersion, paths]);

  const validateRename = useCallback(
    (oldPath: string, newPath: string): string | null => {
      if (newPath === oldPath) return null;
      if (newPath in savedMap) return 'Name already exists';
      return null;
    },
    [savedMap],
  );

  const handleRename = useCallback(
    (oldPath: string, newPath: string) => {
      if (validateRename(oldPath, newPath)) return;
      onRename?.(oldPath, newPath);
      renameBuffer(oldPath, newPath);
      if (activePath === oldPath) setActive(newPath);
    },
    [onRename, renameBuffer, activePath, setActive, validateRename],
  );

  const handleDelete = useCallback(
    (path: string) => {
      onDelete?.(path);
      const wasActive = activePath === path;
      let fallback: string | null = null;
      if (wasActive) {
        const remaining = paths.filter((p) => p !== path);
        fallback = remaining[0] ?? null;
      }
      deleteBuffer(path);
      if (wasActive) setActive(fallback);
    },
    [onDelete, deleteBuffer, activePath, paths, setActive],
  );

  const handleAddFile = useCallback((typeId?: string) => {
    if (!onAddFile) return;
    const returned = onAddFile(typeId);
    if (typeof returned === 'string' && returned) {
      setActive(returned);
      return;
    }
    if (isControlled) return;
    const existing = new Set<string>(paths);
    for (const id of customViewById.keys()) existing.add(id);
    const newName = generateDefaultFilename(existing);
    setSavedMap((prev) => ({ ...prev, [newName]: '' }));
    setActive(newName);
  }, [onAddFile, isControlled, paths, customViewById, setSavedMap, setActive]);

  const innerEditorRef = useRef<MdEditorRef>(null);

  const scrollActiveToHeading = useCallback((index: number) => {
    const editor = innerEditorRef.current?.editor;
    if (!editor) return;
    scrollToHeading(editor, index);
  }, []);

  const handleTocClick = useCallback(
    (path: string, headingIndex: number) => {
      if (path !== activePath) {
        setActive(path);
        requestAnimationFrame(() => {
          requestAnimationFrame(() => scrollActiveToHeading(headingIndex));
        });
      } else {
        scrollActiveToHeading(headingIndex);
      }
    },
    [activePath, setActive, scrollActiveToHeading],
  );

  useImperativeHandle(
    editorRef,
    (): ExplorerRef => ({
      getBuffer,
      getAllBuffers,
      isDirty,
      setActive,
      resetBuffer,
      focusEditor: () => innerEditorRef.current?.focus(),
      scrollToHeading: scrollActiveToHeading,
      getActiveMarkdown: () => innerEditorRef.current?.getMarkdown() ?? '',
      setActiveMarkdown: (md: string) => innerEditorRef.current?.setMarkdown(md),
    }),
    [getBuffer, getAllBuffers, isDirty, setActive, resetBuffer, scrollActiveToHeading],
  );

  const activeContent: string | undefined =
    activePath !== null && activePath !== undefined && !activeCustomView
      ? getBuffer(activePath)
      : undefined;

  const handleEditorChange = useCallback(
    (md: string) => {
      if (activePath === null || activePath === undefined) return;
      updateBuffer(activePath, md);
      onFileChange?.(activePath, md);
    },
    [activePath, updateBuffer, onFileChange],
  );

  const defaultEmpty = <Box sx={{ color: 'text.secondary' }}>Select a file to begin.</Box>;

  const getContent = useCallback((path: string) => getBuffer(path), [getBuffer]);

  return (
    <Stack
      direction="row"
      className={className}
      sx={{
        width: '100%',
        height: '100%',
        flex: 1,
        minHeight: minHeight ? `${minHeight}px` : 0,
        maxHeight: maxHeight ? `${maxHeight}px` : undefined,
        border: '1px solid',
        borderColor: 'divider',
        overflow: 'hidden',
        bgcolor: 'background.paper',
      }}
    >
      <Box
        sx={{
          width: `${sidebarWidth}px`,
          flexShrink: 0,
          borderRight: '1px solid',
          borderColor: 'divider',
          bgcolor: 'background.default',
          display: 'flex',
          flexDirection: 'column',
        }}
      >
        <Sidebar
          searchPlaceholder={searchPlaceholder}
          paths={paths}
          customViews={customViews}
          getContent={getContent}
          contentVersion={bufferVersion}
          activePath={activePath ?? null}
          dirtyPaths={dirtyPaths}
          pendingPaths={pendingPaths}
          chatModifiedPaths={chatModifiedPaths}
          transparentFolders={transparentFolders}
          getFolderIcon={getFolderIcon}
          showHeadings={showHeadings}
          getFileLabel={getFileLabel}
          getFileSortKey={getFileSortKey}
          onActivate={setActive}
          onTocClick={handleTocClick}
          onAddFile={onAddFile ? handleAddFile : undefined}
          addFileMenu={addFileMenu}
          onRename={onRename || !isControlled ? handleRename : undefined}
          onDelete={onDelete || !isControlled ? handleDelete : undefined}
          validateRename={validateRename}
        />
      </Box>
      <Box sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        {activeCustomView ? (
          <Box key={activeCustomView.id} sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
            {activeCustomView.content}
          </Box>
        ) : activePath && activeContent !== undefined ? (
          <ActiveFileEditor
            key={activePath}
            activePath={activePath}
            initialContent={activeContent}
            onChange={handleEditorChange}
            editorProps={editorProps}
            editorRef={innerEditorRef}
            getFileRenderer={getFileRenderer}
          />
        ) : (
          <Box
            sx={{
              flex: 1,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              p: 3,
              textAlign: 'center',
              color: 'text.secondary',
            }}
          >
            {emptyState ?? defaultEmpty}
          </Box>
        )}
      </Box>
    </Stack>
  );
}

export type { FileMap };
