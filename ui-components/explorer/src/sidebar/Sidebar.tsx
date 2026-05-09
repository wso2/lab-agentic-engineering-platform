import { useMemo, useRef, useState } from 'react';
import {
  Box,
  Chip,
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
  MoreVertical,
  Pencil,
  Plus,
  Search,
  Trash2,
  X,
} from '@wso2/oxygen-ui-icons-react';
import type { AddFileMenuItem, TocEntry } from '../types.js';
import { parseToc } from '../toc/parseToc.js';

export interface SidebarProps {
  searchPlaceholder: string;
  paths: string[];
  getContent: (path: string) => string | undefined;
  contentVersion: number;
  activePath: string | null;
  dirtyPaths: Set<string>;
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

function computeDocInfo(path: string, markdown: string): DocInfo {
  // Always show the filename (without the .md/.markdown extension) as the
  // sidebar title — file identity beats document title here. The full TOC
  // (including any H1) stays expandable underneath.
  const parsed = parseToc(markdown);
  const title = path.replace(/\.(md|markdown)$/i, '');
  return { title, toc: parsed, headingCount: parsed.length };
}

export function Sidebar({
  searchPlaceholder,
  paths,
  getContent,
  contentVersion,
  activePath,
  dirtyPaths,
  onActivate,
  onTocClick,
  onAddFile,
  addFileMenu,
  onRename,
  onDelete,
  validateRename,
}: SidebarProps) {
  const [query, setQuery] = useState('');
  const [renamingPath, setRenamingPath] = useState<string | null>(null);
  const [kebab, setKebab] = useState<{ path: string; anchor: HTMLElement } | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState<{ path: string; anchor: HTMLElement } | null>(
    null,
  );
  const [collapsedDocs, setCollapsedDocs] = useState<Set<string>>(() => new Set());
  const [addMenuAnchor, setAddMenuAnchor] = useState<HTMLElement | null>(null);
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
                onClick={(e) => {
                  if (addFileMenu && addFileMenu.items.length > 0) {
                    setAddMenuAnchor(e.currentTarget);
                  } else {
                    onAddFile();
                  }
                }}
              >
                <Plus size={18} />
              </IconButton>
            </Tooltip>
            {addFileMenu && (
              <Menu
                anchorEl={addMenuAnchor}
                open={Boolean(addMenuAnchor)}
                onClose={() => setAddMenuAnchor(null)}
                anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
                transformOrigin={{ vertical: 'top', horizontal: 'right' }}
              >
                {addFileMenu.items.map((item) => (
                  <MenuItem
                    key={item.id}
                    disabled={item.disabled}
                    onClick={() => {
                      setAddMenuAnchor(null);
                      onAddFile(item.id);
                    }}
                    sx={{ minWidth: 220 }}
                  >
                    <Stack direction="column" sx={{ py: 0.25 }}>
                      <Typography variant="body2" sx={{ fontWeight: 500 }}>
                        {item.label}
                      </Typography>
                      {item.description && (
                        <Typography variant="caption" color="text.secondary">
                          {item.description}
                        </Typography>
                      )}
                    </Stack>
                  </MenuItem>
                ))}
              </Menu>
            )}
          </>
        )}
      </Stack>

      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto', px: 0.5, pb: 2 }}>
        {paths.length === 0 ? (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', textAlign: 'center', py: 2 }}>
            No files
          </Typography>
        ) : filteredPaths.length === 0 ? (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', textAlign: 'center', py: 2 }}>
            No matches for "{query}"
          </Typography>
        ) : (
          <Box component="ul" role="tree" sx={{ listStyle: 'none', m: 0, p: 0 }}>
            {filteredPaths.map((path) => {
              const isActive = path === activePath;
              const isDirty = dirtyPaths.has(path);
              const isRenaming = renamingPath === path;
              const info = docInfoByPath.get(path);
              const displayTitle = info?.title ?? path.replace(/\.(md|markdown)$/i, '');
              const toc = info?.toc ?? [];
              const headingCount = info?.headingCount ?? 0;
              const isCollapsed = collapsedDocs.has(path);
              const hasToc = toc.length > 0;

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
                    <FileText size={16} style={{ flexShrink: 0 }} />
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
