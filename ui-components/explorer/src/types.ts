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
