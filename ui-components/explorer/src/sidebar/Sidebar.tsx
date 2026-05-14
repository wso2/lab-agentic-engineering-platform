import { useMemo, useRef, useState } from 'react';
import {
  Box,
  ButtonBase,
  Chip,
  CircularProgress,
  Dialog,
  DialogContent,
  DialogTitle,
  IconButton,
  InputAdornment,
  InputBase,
  ListItemIcon,
  Menu,
  MenuItem,
  Popover,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@wso2/oxygen-ui';
import {
  ChevronRight,
  FileText,
  Folder,
  MoreVertical,
  Pencil,
  Plus,
  Search,
  Trash2,
  X,
} from '@wso2/oxygen-ui-icons-react';
import type { AddFileMenuItem, CustomView, TocEntry } from '../types.js';
import { parseToc } from '../toc/parseToc.js';

// Tree rendering: when any path contains '/', the sidebar groups files
// under virtual folder rows. Folders are derived from paths (not stored
// separately). Clicking a folder toggles its expansion; clicking a leaf
// activates the file like before. For flat usage (requirements page),
// no folder rows are emitted and behaviour is identical to before.
type TreeRow =
  | { kind: 'folder'; path: string; depth: number }
  | { kind: 'file'; path: string; depth: number };

function buildTreeRows(
  paths: string[],
  transparentFolders?: Set<string>,
  getFileSortKey?: (path: string) => number | undefined,
): TreeRow[] {
  const sorted = [...paths].sort((a, b) => {
    // Sort: files at root first, then folders alphabetically, then files
    // alphabetically. This mirrors the design page intent: root
    // `design.md` shows up first, then `components/` subtree.
    const aHasSlash = a.includes('/');
    const bHasSlash = b.includes('/');
    if (aHasSlash !== bHasSlash) return aHasSlash ? 1 : -1;
    // Host-supplied sort key wins over alpha — only consulted when both
    // sides resolve to a number (so unrecognised filenames don't shuffle
    // around the registered ones).
    const keyA = getFileSortKey?.(a);
    const keyB = getFileSortKey?.(b);
    if (keyA !== undefined && keyB !== undefined && keyA !== keyB) {
      return keyA - keyB;
    }
    if (keyA !== undefined && keyB === undefined) return -1;
    if (keyA === undefined && keyB !== undefined) return 1;
    return a.localeCompare(b);
  });
  const rows: TreeRow[] = [];
  const emitted = new Set<string>();
  for (const path of sorted) {
    const parts = path.split('/');
    let prefix = '';
    // `visibleDepth` tracks indent shown to the user. It diverges from the
    // loop index whenever we pass through a transparent folder (the folder
    // is omitted and its children promote one level up).
    let visibleDepth = 0;
    for (let i = 0; i < parts.length - 1; i++) {
      prefix = prefix ? `${prefix}/${parts[i]}` : parts[i];
      if (transparentFolders?.has(prefix)) continue;
      if (!emitted.has(prefix)) {
        emitted.add(prefix);
        rows.push({ kind: 'folder', path: prefix, depth: visibleDepth });
      }
      visibleDepth++;
    }
    rows.push({ kind: 'file', path, depth: visibleDepth });
  }
  return rows;
}

function isAncestorCollapsed(path: string, collapsed: Set<string>): boolean {
  const parts = path.split('/');
  for (let i = 1; i < parts.length; i++) {
    const ancestor = parts.slice(0, i).join('/');
    if (collapsed.has(ancestor)) return true;
  }
  return false;
}

function lastSegment(path: string): string {
  const slash = path.lastIndexOf('/');
  return slash < 0 ? path : path.slice(slash + 1);
}

export interface SidebarProps {
  searchPlaceholder: string;
  paths: string[];
  /** Non-file entries pinned above the file list; not renameable / deletable. */
  customViews?: CustomView[];
  getContent: (path: string) => string | undefined;
  contentVersion: number;
  activePath: string | null;
  dirtyPaths: Set<string>;
  /** Paths in this set render a spinner instead of their file/folder icon. */
  pendingPaths?: Set<string>;
  /** Folder paths that should not appear in the tree (children promote up). */
  transparentFolders?: Set<string>;
  /** Override the folder icon per-path. Return undefined to use the default. */
  getFolderIcon?: (path: string) => React.ReactNode | undefined;
  /** Render parsed headings under each file as nested rows. Default true. */
  showHeadings?: boolean;
  /** Override the displayed label for specific paths. Falls back to the
   *  extension-stripped filename when the function returns undefined. */
  getFileLabel?: (path: string) => string | undefined;
  /** Optional sort key for the sidebar tree (lower = earlier). When
   *  undefined, files fall back to case-insensitive alphabetical order. */
  getFileSortKey?: (path: string) => number | undefined;
  onActivate: (path: string) => void;
  onTocClick: (path: string, headingIndex: number) => void;
  onAddFile?: (typeId?: string) => void;
  addFileMenu?: { items: AddFileMenuItem[] };
  onRename?: (oldPath: string, newPath: string) => void;
  onDelete?: (path: string) => void;
  validateRename?: (oldPath: string, newPath: string) => string | null;
}

interface DocInfo {
  title: string;
  toc: TocEntry[];
  headingCount: number;
}

// Extensions whose suffix is hidden in the sidebar label.
const STRIP_EXT_RE = /\.(md|markdown|excalidraw)$/i;

function stripExtension(path: string): string {
  return path.replace(STRIP_EXT_RE, '');
}

function computeDocInfo(path: string, markdown: string): DocInfo {
  // Always show the filename (without its known extension) as the sidebar
  // title — file identity beats document title here. The full TOC
  // (including any H1) stays expandable underneath. Non-markdown files
  // (e.g. *.excalidraw) parse to an empty TOC and render without an
  // outline.
  const parsed = parseToc(markdown);
  const title = stripExtension(path);
  return { title, toc: parsed, headingCount: parsed.length };
}

export function Sidebar({
  searchPlaceholder,
  paths,
  customViews,
  getContent,
  contentVersion,
  activePath,
  dirtyPaths,
  pendingPaths,
  transparentFolders,
  getFolderIcon,
  showHeadings = true,
  getFileLabel,
  getFileSortKey,
  onActivate,
  onTocClick,
  onAddFile,
  addFileMenu,
  onRename,
  onDelete,
  validateRename,
}: SidebarProps) {
  // A folder is "pending" if any of the pending paths sits underneath it.
  // Cheap on small trees; kept inline so callers don't have to compute it.
  const folderIsPending = (folderPath: string): boolean => {
    if (!pendingPaths || pendingPaths.size === 0) return false;
    const prefix = folderPath + '/';
    for (const p of pendingPaths) {
      if (p === folderPath || p.startsWith(prefix)) return true;
    }
    return false;
  };
  const [query, setQuery] = useState('');
  const [renamingPath, setRenamingPath] = useState<string | null>(null);
  const [kebab, setKebab] = useState<{ path: string; anchor: HTMLElement } | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState<{ path: string; anchor: HTMLElement } | null>(
    null,
  );
  const [collapsedDocs, setCollapsedDocs] = useState<Set<string>>(() => new Set());
  const [collapsedFolders, setCollapsedFolders] = useState<Set<string>>(() => new Set());
  const [addDialogOpen, setAddDialogOpen] = useState(false);
  const rowRefs = useRef<Map<string, HTMLDivElement>>(new Map());

  const docInfoByPath = useMemo(() => {
    const map = new Map<string, DocInfo>();
    for (const p of paths) {
      map.set(p, computeDocInfo(p, getContent(p) ?? ''));
    }
    return map;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paths, contentVersion]);

  const filteredPaths = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return paths;
    return paths.filter((p) => {
      if (p.toLowerCase().includes(q)) return true;
      const info = docInfoByPath.get(p);
      if (!info) return false;
      if (info.title.toLowerCase().includes(q)) return true;
      return info.toc.some((e) => e.text.toLowerCase().includes(q));
    });
  }, [paths, query, docInfoByPath]);

  const toggleDocCollapsed = (path: string) => {
    setCollapsedDocs((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  const toggleFolderCollapsed = (path: string) => {
    setCollapsedFolders((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  const treeRows = useMemo(() => {
    const rows = buildTreeRows(filteredPaths, transparentFolders, getFileSortKey);
    return rows.filter((row) => {
      if (row.depth === 0) return true;
      return !isAncestorCollapsed(row.path, collapsedFolders);
    });
  }, [filteredPaths, collapsedFolders, transparentFolders, getFileSortKey]);

  const usesTree = useMemo(() => filteredPaths.some((p) => p.includes('/')), [filteredPaths]);

  const filteredCustomViews = useMemo(() => {
    const list = customViews ?? [];
    const q = query.trim().toLowerCase();
    if (!q) return list;
    return list.filter((v) => v.label.toLowerCase().includes(q) || v.id.toLowerCase().includes(q));
  }, [customViews, query]);

  const hasAnyContent = filteredCustomViews.length > 0 || filteredPaths.length > 0;

  const beginRename = (path: string) => {
    setRenamingPath(path);
    setKebab(null);
  };

  const beginDelete = (path: string) => {
    const el = rowRefs.current.get(path);
    if (!el) return;
    setDeleteConfirm({ path, anchor: el });
    setKebab(null);
  };

  const commitRename = (oldPath: string, newName: string) => {
    if (!onRename) return;
    if (newName === oldPath) {
      setRenamingPath(null);
      return;
    }
    onRename(oldPath, newName);
    setRenamingPath(null);
  };

  const hasActions = Boolean(onRename || onDelete);

  return (
    <Stack direction="column" sx={{ height: '100%', minHeight: 0 }}>
      <Stack
        direction="row"
        spacing={0.5}
        alignItems="center"
        sx={{ px: 1, pt: 1.25, pb: 1, flexShrink: 0 }}
      >
        <InputBase
          fullWidth
          value={query}
          placeholder={searchPlaceholder}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && query) {
              e.preventDefault();
              e.stopPropagation();
              setQuery('');
            }
          }}
          startAdornment={
            <InputAdornment position="start">
              <Search size={15} />
            </InputAdornment>
          }
          endAdornment={
            query ? (
              <InputAdornment position="end">
                <IconButton
                  size="small"
                  aria-label="Clear search"
                  onClick={() => setQuery('')}
                  sx={{ width: 20, height: 20 }}
                >
                  <X size={14} />
                </IconButton>
              </InputAdornment>
            ) : undefined
          }
          sx={{
            flex: 1,
            fontSize: 13,
            bgcolor: 'action.hover',
            borderRadius: 999,
            px: 1.25,
            py: 0.5,
            '&.Mui-focused': { bgcolor: 'background.paper', boxShadow: (t) => `0 0 0 1px ${t.palette.primary.main}` },
            '& input': { py: 0.25 },
          }}
        />
        {onAddFile && (
          <>
            <Tooltip title="Add file">
              <IconButton
                size="small"
                aria-label="Add file"
                onClick={() => {
                  if (addFileMenu && addFileMenu.items.length > 0) {
                    setAddDialogOpen(true);
                  } else {
                    onAddFile();
                  }
                }}
              >
                <Plus size={18} />
              </IconButton>
            </Tooltip>
            {addFileMenu && (
              <Dialog
                open={addDialogOpen}
                onClose={() => setAddDialogOpen(false)}
                maxWidth="xs"
                fullWidth
                sx={{
                  // Force a fully opaque Paper. slotProps.paper didn't
                  // stick — Oxygen UI's theme paints the Dialog with a
                  // translucent surface tint at higher MUI elevation,
                  // which let the canvas bleed through. Pin the surface
                  // here with `!important` so neither the theme overlay
                  // nor MUI's elevation gradient can override it.
                  '& .MuiDialog-paper': {
                    backgroundColor: '#ffffff !important',
                    backgroundImage: 'none !important',
                    opacity: '1 !important',
                  },
                  '& .MuiBackdrop-root': {
                    backgroundColor: 'rgba(0, 0, 0, 0.5) !important',
                  },
                }}
              >
                <DialogTitle>Add document</DialogTitle>
                <DialogContent dividers>
                  <Stack direction="column" spacing={1}>
                    {addFileMenu.items.map((item) => (
                      <ButtonBase
                        key={item.id}
                        disabled={item.disabled}
                        onClick={() => {
                          setAddDialogOpen(false);
                          onAddFile(item.id);
                        }}
                        sx={{
                          justifyContent: 'flex-start',
                          textAlign: 'left',
                          p: 1.25,
                          borderRadius: 1,
                          border: '1px solid',
                          borderColor: 'divider',
                          width: '100%',
                          '&:hover': { bgcolor: 'action.hover' },
                          '&.Mui-disabled': { opacity: 0.5 },
                        }}
                      >
                        <Stack direction="column" sx={{ width: '100%' }}>
                          <Typography variant="body1" sx={{ fontWeight: 700 }}>
                            {item.label}
                          </Typography>
                          {item.description && (
                            <Typography variant="caption" color="text.secondary">
                              {item.description}
                            </Typography>
                          )}
                        </Stack>
                      </ButtonBase>
                    ))}
                  </Stack>
                </DialogContent>
              </Dialog>
            )}
          </>
        )}
      </Stack>

      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto', px: 0.5, pb: 2 }}>
        {paths.length === 0 && (customViews ?? []).length === 0 ? (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', textAlign: 'center', py: 2 }}>
            No files
          </Typography>
        ) : !hasAnyContent ? (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', textAlign: 'center', py: 2 }}>
            No matches for "{query}"
          </Typography>
        ) : (
          <Box component="ul" role="tree" sx={{ listStyle: 'none', m: 0, p: 0 }}>
            {filteredCustomViews.map((view) => {
              const isActive = view.id === activePath;
              return (
                <Box component="li" key={`custom:${view.id}`} role="treeitem" aria-selected={isActive} sx={{ listStyle: 'none' }}>
                  <Box
                    onClick={() => onActivate(view.id)}
                    sx={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 1,
                      mx: 1,
                      my: 0.25,
                      px: 1,
                      pl: '8px',
                      py: 0.75,
                      borderRadius: 999,
                      cursor: 'pointer',
                      color: isActive ? 'primary.main' : 'text.primary',
                      bgcolor: isActive
                        ? 'color-mix(in srgb, currentColor 12%, transparent)'
                        : 'transparent',
                      fontWeight: isActive ? 500 : 400,
                      transition: 'background-color 80ms ease',
                      '&:hover': {
                        bgcolor: isActive
                          ? 'color-mix(in srgb, currentColor 18%, transparent)'
                          : 'action.hover',
                      },
                    }}
                  >
                    <Box sx={{ width: 22, height: 22, flexShrink: 0 }} />
                    <Box
                      component="span"
                      sx={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}
                    >
                      {view.icon ?? <FileText size={16} />}
                    </Box>
                    <Typography
                      component="span"
                      title={view.label}
                      sx={{
                        flex: 1,
                        minWidth: 0,
                        fontSize: 13,
                        fontWeight: 'inherit',
                        color: 'inherit',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {view.label}
                    </Typography>
                  </Box>
                </Box>
              );
            })}
            {treeRows.map((row) => {
              if (row.kind === 'folder') {
                const isFolderCollapsed = collapsedFolders.has(row.path);
                return (
                  <Box component="li" key={`folder:${row.path}`} role="treeitem" aria-expanded={!isFolderCollapsed} sx={{ listStyle: 'none' }}>
                    <Box
                      onClick={() => toggleFolderCollapsed(row.path)}
                      sx={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: 1,
                        mx: 1,
                        my: 0.25,
                        px: 1,
                        pl: `${8 + row.depth * 14}px`,
                        py: 0.75,
                        borderRadius: 999,
                        cursor: 'pointer',
                        color: 'text.secondary',
                        '&:hover': { bgcolor: 'action.hover', color: 'text.primary' },
                      }}
                    >
                      <Box
                        component="span"
                        sx={{
                          width: 22,
                          height: 22,
                          display: 'inline-flex',
                          alignItems: 'center',
                          justifyContent: 'center',
                          color: 'inherit',
                          transition: 'transform 120ms ease',
                          transform: isFolderCollapsed ? 'rotate(0deg)' : 'rotate(90deg)',
                        }}
                      >
                        <ChevronRight size={14} />
                      </Box>
                      {folderIsPending(row.path) ? (
                        <CircularProgress size={14} sx={{ flexShrink: 0 }} />
                      ) : (
                        getFolderIcon?.(row.path) ?? <Folder size={16} style={{ flexShrink: 0 }} />
                      )}
                      <Typography
                        component="span"
                        title={row.path}
                        sx={{
                          flex: 1,
                          minWidth: 0,
                          fontSize: 13,
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {lastSegment(row.path)}
                      </Typography>
                    </Box>
                  </Box>
                );
              }

              const path = row.path;
              const isActive = path === activePath;
              const isDirty = dirtyPaths.has(path);
              const isRenaming = renamingPath === path;
              const info = docInfoByPath.get(path);
              const fallbackLabel = usesTree ? stripExtension(lastSegment(path)) : stripExtension(path);
              // A custom label always wins over the filename derivation —
              // host can surface `openapi.yaml` as "API Spec" without
              // touching disk semantics.
              const customLabel = getFileLabel?.(path);
              const displayTitle = customLabel ?? (usesTree ? fallbackLabel : (info?.title ?? fallbackLabel));
              const toc = info?.toc ?? [];
              const headingCount = info?.headingCount ?? 0;
              const isCollapsed = collapsedDocs.has(path);
              // `showHeadings` disables the in-tree TOC entirely — when it's
              // off there's nothing to expand, so the chevron + nested list
              // both vanish via this single gate.
              const hasToc = showHeadings && toc.length > 0;

              return (
                <Box component="li" key={path} role="treeitem" aria-selected={isActive} aria-expanded={hasToc ? !isCollapsed : undefined} sx={{ listStyle: 'none' }}>
                  <Box
                    ref={(el: HTMLDivElement | null) => {
                      if (el) rowRefs.current.set(path, el);
                      else rowRefs.current.delete(path);
                    }}
                    onClick={() => onActivate(path)}
                    sx={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 1,
                      mx: 1,
                      my: 0.25,
                      px: 1,
                      pl: `${8 + row.depth * 14}px`,
                      py: 0.75,
                      borderRadius: 999,
                      cursor: 'pointer',
                      color: isActive ? 'primary.main' : 'text.primary',
                      bgcolor: isActive
                        ? 'color-mix(in srgb, currentColor 12%, transparent)'
                        : 'transparent',
                      fontWeight: isActive ? 500 : 400,
                      transition: 'background-color 80ms ease',
                      '&:hover': {
                        bgcolor: isActive
                          ? 'color-mix(in srgb, currentColor 18%, transparent)'
                          : 'action.hover',
                      },
                    }}
                  >
                    <IconButton
                      size="small"
                      disabled={!hasToc}
                      aria-label={isCollapsed ? 'Expand outline' : 'Collapse outline'}
                      aria-expanded={!isCollapsed}
                      onClick={(e) => {
                        e.stopPropagation();
                        if (hasToc) toggleDocCollapsed(path);
                      }}
                      sx={{
                        width: 22,
                        height: 22,
                        color: 'inherit',
                        visibility: hasToc ? 'visible' : 'hidden',
                        transition: 'transform 120ms ease',
                        transform: isCollapsed ? 'rotate(0deg)' : 'rotate(90deg)',
                      }}
                    >
                      <ChevronRight size={14} />
                    </IconButton>
                    {pendingPaths?.has(path) ? (
                      <CircularProgress size={14} sx={{ flexShrink: 0 }} />
                    ) : (
                      <FileText size={16} style={{ flexShrink: 0 }} />
                    )}
                    {isRenaming ? (
                      <InlineRenameField
                        initialName={path}
                        validate={(newName) => (validateRename ? validateRename(path, newName) : null)}
                        onCommit={(newName) => commitRename(path, newName)}
                        onCancel={() => setRenamingPath(null)}
                      />
                    ) : (
                      <Typography
                        component="span"
                        title={path}
                        sx={{
                          flex: 1,
                          minWidth: 0,
                          fontSize: 13,
                          fontWeight: 'inherit',
                          color: 'inherit',
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {displayTitle}
                      </Typography>
                    )}
                    {isDirty && !isRenaming && (
                      <Tooltip title="Unsaved changes">
                        <Box
                          aria-label="Unsaved changes"
                          sx={{
                            width: 7,
                            height: 7,
                            borderRadius: '50%',
                            bgcolor: 'primary.main',
                            flexShrink: 0,
                          }}
                        />
                      </Tooltip>
                    )}
                    {isActive && !isRenaming && headingCount > 0 && (
                      <Chip
                        label={headingCount}
                        size="small"
                        sx={{
                          height: 20,
                          fontSize: 11,
                          fontWeight: 500,
                          bgcolor: 'primary.main',
                          color: 'primary.contrastText',
                          '& .MuiChip-label': { px: 0.75 },
                        }}
                      />
                    )}
                    {isActive && !isRenaming && hasActions && (
                      <IconButton
                        size="small"
                        aria-label="File actions"
                        onClick={(e) => {
                          e.stopPropagation();
                          setKebab({ path, anchor: e.currentTarget as HTMLElement });
                        }}
                        sx={{ width: 22, height: 22, color: 'inherit' }}
                      >
                        <MoreVertical size={14} />
                      </IconButton>
                    )}
                  </Box>
                  {hasToc && !isCollapsed && (
                    <Box
                      component="ul"
                      role="group"
                      sx={{
                        listStyle: 'none',
                        m: 0,
                        p: 0,
                        position: 'relative',
                        '&::before': {
                          content: '""',
                          position: 'absolute',
                          left: '28px',
                          top: '4px',
                          bottom: '4px',
                          width: '1px',
                          bgcolor: 'divider',
                        },
                      }}
                    >
                      {toc.map((entry) => (
                        <Box
                          component="li"
                          key={entry.index}
                          role="treeitem"
                          title={entry.text}
                          onClick={(e) => {
                            e.stopPropagation();
                            onTocClick(path, entry.index);
                          }}
                          sx={{
                            mx: 1,
                            py: 0.6,
                            pr: 1.5,
                            pl: `${28 + entry.level * 10}px`,
                            borderRadius: 2,
                            cursor: 'pointer',
                            fontSize: 13,
                            color: 'text.secondary',
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                            lineHeight: 1.3,
                            '&:hover': { bgcolor: 'action.hover', color: 'text.primary' },
                          }}
                        >
                          {entry.text}
                        </Box>
                      ))}
                    </Box>
                  )}
                </Box>
              );
            })}
          </Box>
        )}
      </Box>

      <Menu
        anchorEl={kebab?.anchor ?? null}
        open={Boolean(kebab)}
        onClose={() => setKebab(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
        slotProps={{ paper: { sx: { minWidth: 160 } } }}
      >
        {onRename && (
          <MenuItem dense onClick={() => kebab && beginRename(kebab.path)}>
            <ListItemIcon><Pencil size={15} /></ListItemIcon>
            Rename
          </MenuItem>
        )}
        {onDelete && (
          <MenuItem dense onClick={() => kebab && beginDelete(kebab.path)} sx={{ color: 'error.main' }}>
            <ListItemIcon sx={{ color: 'inherit' }}><Trash2 size={15} /></ListItemIcon>
            Delete
          </MenuItem>
        )}
      </Menu>

      <Popover
        open={Boolean(deleteConfirm)}
        anchorEl={deleteConfirm?.anchor ?? null}
        onClose={() => setDeleteConfirm(null)}
        anchorOrigin={{ vertical: 'center', horizontal: 'right' }}
        transformOrigin={{ vertical: 'center', horizontal: 'left' }}
      >
        {deleteConfirm && onDelete && (
          <Box sx={{ p: 2, maxWidth: 280 }}>
            <Typography variant="body2" sx={{ mb: 0.5 }}>
              Delete <Box component="code" sx={{ fontFamily: 'monospace', fontSize: 12 }}>{deleteConfirm.path}</Box>?
            </Typography>
            <Typography variant="caption" color="text.secondary">
              This cannot be undone.
            </Typography>
            <Stack direction="row" spacing={1} justifyContent="flex-end" sx={{ mt: 1.5 }}>
              <Box
                component="button"
                type="button"
                onClick={() => setDeleteConfirm(null)}
                sx={{
                  px: 1.5,
                  py: 0.5,
                  borderRadius: 1,
                  border: '1px solid',
                  borderColor: 'divider',
                  bgcolor: 'background.paper',
                  fontSize: 12,
                  cursor: 'pointer',
                  fontFamily: 'inherit',
                }}
              >
                Cancel
              </Box>
              <Box
                component="button"
                type="button"
                autoFocus
                onClick={() => {
                  onDelete(deleteConfirm.path);
                  setDeleteConfirm(null);
                }}
                sx={{
                  px: 1.5,
                  py: 0.5,
                  borderRadius: 1,
                  border: '1px solid',
                  borderColor: 'error.main',
                  bgcolor: 'error.main',
                  color: 'error.contrastText',
                  fontSize: 12,
                  cursor: 'pointer',
                  fontFamily: 'inherit',
                }}
              >
                Delete
              </Box>
            </Stack>
          </Box>
        )}
      </Popover>
    </Stack>
  );
}

interface InlineRenameFieldProps {
  initialName: string;
  validate: (newName: string) => string | null;
  onCommit: (newName: string) => void;
  onCancel: () => void;
}

function InlineRenameField({ initialName, validate, onCommit, onCancel }: InlineRenameFieldProps) {
  const [value, setValue] = useState(initialName);
  const [error, setError] = useState<string | null>(null);

  const tryCommit = () => {
    const trimmed = value.trim();
    if (!trimmed || trimmed === initialName) {
      onCancel();
      return;
    }
    const err = validate(trimmed);
    if (err) {
      setError(err);
      return;
    }
    onCommit(trimmed);
  };

  return (
    <TextField
      autoFocus
      size="small"
      value={value}
      error={Boolean(error)}
      helperText={error ?? undefined}
      onChange={(e) => {
        setValue(e.target.value);
        if (error) setError(null);
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          tryCommit();
        } else if (e.key === 'Escape') {
          e.preventDefault();
          onCancel();
        }
        e.stopPropagation();
      }}
      onBlur={tryCommit}
      onClick={(e) => e.stopPropagation()}
      onFocus={(e) => {
        const el = e.target;
        const dot = initialName.lastIndexOf('.');
        if (dot > 0) el.setSelectionRange(0, dot);
        else el.select();
      }}
      sx={{
        flex: 1,
        minWidth: 0,
        '& .MuiInputBase-input': { fontSize: 13, py: 0.25, px: 0.75 },
        '& .MuiOutlinedInput-root': { borderRadius: 1 },
      }}
    />
  );
}
