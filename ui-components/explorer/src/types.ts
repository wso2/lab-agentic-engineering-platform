import type React from 'react';
import type { MdEditorProps } from '@asdlc/md-editor';

/** Flat map: file id -> markdown content. The key is the display name (no folder semantics). */
export type FileMap = Record<string, string>;

/** One choice in the "+" button's add-file menu. */
export interface AddFileMenuItem {
  id: string;
  label: string;
  description?: string;
  disabled?: boolean;
}

/**
 * A non-file entry in the Explorer. Custom views appear pinned above the
 * regular file list in the sidebar; selecting one renders `content` in the
 * editor pane instead of an `ActiveFileEditor`. View ids are used as the
 * `activePath` sentinel and must not collide with real file paths — prefer a
 * stable, namespaced id like `cell-diagram` or `__architecture__`.
 *
 * Custom views bypass the file-buffer system (no dirty tracking, no
 * `onFileChange`) and are not rename-able or delete-able.
 */
export interface CustomView {
  id: string;
  label: string;
  icon?: React.ReactNode;
  content: React.ReactNode;
}

/** A heading entry parsed from a markdown document. */
export interface TocEntry {
  /** Heading depth, 1..6. */
  level: number;
  /** Heading text. */
  text: string;
  /** 0-based ordinal among all headings in the document. */
  index: number;
}

export type ExplorerEditorProps = Partial<
  Pick<
    MdEditorProps,
    | 'readOnly'
    | 'placeholder'
    | 'showToolbar'
    | 'toolbarGroups'
    | 'contentMaxWidth'
    | 'toolbarRightContent'
    | 'collab'
    | 'baseMarkdown'
  >
>;

export interface ExplorerProps {
  // --- file data (controlled / uncontrolled) ---
  /** Controlled saved-content map keyed by file name/id. */
  files?: FileMap;
  /** Initial files for uncontrolled mode. */
  defaultFiles?: FileMap;
  /** Fires on every editor keystroke for the active file. */
  onFileChange?: (path: string, md: string) => void;

  // --- active file ---
  activePath?: string | null;
  defaultActivePath?: string;
  onActivePathChange?: (path: string | null) => void;

  // --- file operations ---
  /**
   * When set, a "+" button is shown in the sidebar header. If `addFileMenu`
   * is also set, the button anchors a menu and the callback receives the
   * selected item's `id`; otherwise the callback is invoked directly.
   * Should return a brand new, unique file id/name (controlled) or let the
   * component add it internally (uncontrolled).
   */
  onAddFile?: (typeId?: string) => string | undefined | void;
  /**
   * When provided, the "+" button opens a menu of choices instead of
   * invoking `onAddFile()` directly. The selected item's id is passed to
   * `onAddFile(typeId)` so callers can compute the right filename per type.
   */
  addFileMenu?: { items: AddFileMenuItem[] };
  onRename?: (oldPath: string, newPath: string) => void;
  onDelete?: (path: string) => void;

  // --- custom views ---
  /**
   * Non-file entries pinned above the file list. Selecting one renders its
   * `content` in the editor pane. See {@link CustomView}.
   */
  customViews?: CustomView[];

  /**
   * Paths whose content is still being generated (e.g. by a streaming agent).
   * Files in this set render with a spinner instead of their file icon; folders
   * whose descendants are in the set render a spinner too. The set is purely
   * presentational — caller still controls what's in `files`.
   */
  pendingPaths?: Set<string>;

  /**
   * Paths currently in a "modified by chat" state. Sidebar rows tint with the
   * indigo accent and append a compact `+N/-M` counter so the user can spot
   * which other files in the project have pending changes without hovering.
   * The map value is the per-file line-diff summary against the session
   * baseline. Active selection styling still wins on top of the tint.
   *
   * Purely presentational — the host owns the modified-set lifecycle and the
   * Accept / Revert plumbing.
   */
  chatModifiedPaths?: Map<string, { added: number; removed: number }>;

  /**
   * Folder paths that should not appear as their own row in the tree. Their
   * children promote one level up. Underlying paths in `files` are
   * unchanged — only the visual nesting collapses. Example: pass
   * `new Set(['components'])` so `components/foo/design.md` displays under a
   * top-level `foo` folder.
   */
  transparentFolders?: Set<string>;

  /**
   * Custom icon per folder path. Receives the folder's real path (not the
   * displayed segment) so callers can disambiguate. Return `undefined` to
   * fall back to the default folder icon.
   */
  getFolderIcon?: (path: string) => React.ReactNode | undefined;

  /**
   * Whether to render parsed markdown headings as nested entries under each
   * file in the sidebar. Default: true.
   */
  showHeadings?: boolean;

  /**
   * Optional override for the right-pane editor. When `getFileRenderer`
   * returns a non-null React node for a given path, that node is rendered
   * instead of the default markdown/Excalidraw editor. The saved content is
   * passed as the second argument so callers can parse it without
   * re-fetching. Returning `undefined` falls through to the default chain.
   */
  getFileRenderer?: (path: string, content: string) => React.ReactNode | undefined;

  /**
   * Optional override for the file label displayed in the sidebar tree.
   * Receives the file's real path; return `undefined` to fall back to the
   * default (extension-stripped filename). Useful when on-disk names are
   * implementation details (`openapi.yaml`) and the user-facing label
   * should read differently ("API Spec").
   */
  getFileLabel?: (path: string) => string | undefined;

  /**
   * Optional sort key for the sidebar tree. Lower numbers sort earlier.
   * Files that share a key (or for which the callback returns `undefined`)
   * fall back to case-insensitive alphabetical order. Useful when the host
   * has a domain-specific order (e.g. `requirements.md` always first,
   * generated docs in registry order).
   */
  getFileSortKey?: (path: string) => number | undefined;

  // --- layout / style ---
  /** Placeholder shown in the sidebar search input. Default: "Search documents" */
  searchPlaceholder?: string;
  /** Sidebar width in px. Default: 280 */
  sidebarWidth?: number;
  /** Minimum overall height in px. Default: 400 */
  minHeight?: number;
  /** Maximum overall height in px (scrolls beyond). */
  maxHeight?: number;
  /** Additional CSS class on the root container. */
  className?: string;
  /** Rendered when no file is active. */
  emptyState?: React.ReactNode;

  /** Props forwarded to the underlying MdEditor. */
  editorProps?: ExplorerEditorProps;

  /** Imperative ref. */
  editorRef?: React.Ref<ExplorerRef>;
}

export interface ExplorerRef {
  /** Current in-memory buffer for a path (may be dirty). */
  getBuffer(path: string): string | undefined;
  /** All buffers, keyed by path. */
  getAllBuffers(): FileMap;
  /** True if the buffer differs from the saved content in `files`. */
  isDirty(path: string): boolean;
  /** Programmatically set the active file. */
  setActive(path: string | null): void;
  /** Revert the buffer for `path` back to the saved content. */
  resetBuffer(path: string): void;
  /** Focus the underlying MdEditor. */
  focusEditor(): void;
  /** Scroll the Nth heading (by parseToc index) of the active file into view. */
  scrollToHeading(index: number): void;
  /** Read current markdown directly from the underlying MdEditor (uncontrolled / collab mode). */
  getActiveMarkdown(): string;
  /** Replace the active editor content (e.g. seed a fresh collab room or revert on cancel). */
  setActiveMarkdown(md: string): void;
}
