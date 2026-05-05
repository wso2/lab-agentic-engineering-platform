# MdExplorer Component Specification

## Overview

A React component that pairs a Google Docs-style "Document tabs" sidebar
with an `MdEditor` from `@asdlc/md-editor`. The sidebar is a **flat list of
files** — no folders, no path separators interpreted. Beneath each file, the
component parses the markdown's headings and renders a **live Table of
Contents (ToC)** tree nested under that file. Clicking a ToC entry scrolls
the editor to that heading.

`MdExplorer` composes `MdEditor` — it does not reimplement the editor
surface.

### Design Principles

1. **Files are flat** — the `FileMap` is `Record<string, string>` where the key is the displayed filename; no `/` folder semantics
2. **ToC is derived, not stored** — parsed live from current buffer content; updates as the user types
3. **One active file** — clicking a file row switches the editor; clicking a ToC entry also switches then scrolls
4. **Controlled or uncontrolled** — mirrors `MdEditor`'s `value`/`defaultValue` model
5. **Actions on the active row** — kebab (⋮) menu holds rename/delete; "+" in the header adds a file
6. **Consumer owns persistence** — the component never writes to disk; it emits callbacks and lets the consumer decide

### Technology

- Depends on `@asdlc/md-editor` (`workspace:*`)
- No MUI, no Oxygen UI, no theme context — styling via a single injected `<style>` tag
- Peer: `react ^19.0.0`, `react-dom ^19.0.0`

---

## Component API

### MdExplorer Props

```typescript
/** Flat map: filename -> markdown content. */
type FileMap = Record<string, string>;

interface MdExplorerProps {
  // --- file data (controlled / uncontrolled) ---
  files?: FileMap;
  defaultFiles?: FileMap;
  onFileChange?: (path: string, md: string) => void;

  // --- active file ---
  activePath?: string | null;
  defaultActivePath?: string;
  onActivePathChange?: (path: string | null) => void;

  // --- file operations ---
  /**
   * When set, a "+" button is shown in the sidebar header. May return the
   * new filename; in uncontrolled mode, if undefined is returned, an
   * "Untitled N.md" file is generated and activated.
   */
  onAddFile?: () => string | undefined | void;
  onRename?: (oldPath: string, newPath: string) => void;
  onDelete?: (path: string) => void;

  // --- layout / style ---
  sidebarTitle?: string;           // default "Document tabs"
  sidebarWidth?: number;           // default 280
  minHeight?: number;              // default 400
  maxHeight?: number;
  className?: string;
  emptyState?: React.ReactNode;

  editorProps?: Partial<Pick<MdEditorProps,
    'readOnly' | 'placeholder' | 'showToolbar' | 'toolbarGroups'>>;

  editorRef?: React.Ref<MdExplorerRef>;
}
```

### Imperative Ref

```typescript
interface MdExplorerRef {
  getBuffer(path: string): string | undefined;
  getAllBuffers(): FileMap;
  isDirty(path: string): boolean;
  setActive(path: string | null): void;
  resetBuffer(path: string): void;
  focusEditor(): void;
  /** Scroll the Nth heading of the active file (by parseToc index) into view. */
  scrollToHeading(index: number): void;
}
```

### TocEntry

```typescript
interface TocEntry {
  level: number;   // 1..6
  text: string;
  index: number;   // 0-based ordinal among all headings in the document
}

/** Public utility — same parser used internally. */
function parseToc(markdown: string): TocEntry[];
```

---

## Sidebar Layout

```
┌──────────────────────────────┐
│  Document tabs          +    │  ← header with add-file button
│                              │
│  📄 Other file               │
│  📄 Plan on Shipping A…  7 ⋮│  ← active: pill bg, count badge, kebab
│     │ Shipping Agent Skills  │
│     │ Installation Options   │
│     │    Option 1…           │  ← indented by heading level
│     │       Version mgmt     │
│     │ Repo Structure         │
│     │    File Descriptions   │
│     │       Root Level       │
│     │ References             │
│                              │
└──────────────────────────────┘
```

- **Files** listed alphabetically, case-insensitive
- **Active file**: rounded-pill background (blue tint), heading-count badge, kebab menu button
- **Inactive files**: plain text with document icon; hover shows a faint background
- **ToC** under every file, always visible — indented by heading level (`paddingLeft = 16 + (level-1) * 14` px)
- **Left guide rail** (thin vertical line) runs through each file's ToC section (CSS `::before` on `.md-explorer-toc`)

### Count Badge

The number shown next to the active file's name equals the count of parsed
headings in that file. Updates live as the user types.

### Kebab Menu

Appears on the active file. Items (shown only if the corresponding handler
is set):

- **Rename** — opens inline rename input
- **Delete** — opens inline confirm popover

If neither `onRename` nor `onDelete` is passed, no kebab button renders.

### Add-File Button

The "+" in the header is shown only when `onAddFile` is passed.

- If `onAddFile` returns a string, that filename is activated.
- If it returns `undefined`/`void` and the component is **uncontrolled**, a
  default `Untitled N.md` is generated, added to internal state, and activated.
- In **controlled mode** with a void return, the component does nothing — the
  consumer is expected to update `files` externally.

---

## ToC Parsing & Navigation

### Parsing

`parseToc(md)` scans lines for `^(#{1,6})\s+(.+)$`, skipping fenced code
blocks (``` or `~~~`). ATX-style only — setext headings are not supported
(MdEditor does not emit them).

Each entry carries its level, text, and a 0-based ordinal `index` among all
headings in the document.

### Live Updates

As the user types, the active file's buffer changes (`useFileBuffers.bufferVersion`
bumps). The sidebar re-parses ToC via a `contentVersion` prop passed to its
`useMemo` — so adding or removing a heading in the editor updates the
sidebar immediately.

### Scrolling

Clicking a ToC entry calls `onTocClick(path, index)` internally. If the file
isn't active, the editor switches to it (remounts via `key={activePath}`),
then after two `requestAnimationFrame` ticks scrolls to the Nth heading via
`editor.view.nodeDOM(pos).scrollIntoView()`.

For programmatic use, `MdExplorerRef.scrollToHeading(index)` does the same
for the currently-active file.

---

## Buffer Model

Unchanged from initial design — per-path in-memory buffers, dirty-flag
comparison against saved content, survival across file switches, user-work
protection on external saved-content updates.

| Situation | Behavior |
|-----------|----------|
| User edits active file | Buffer updates each keystroke; `onFileChange(path, md)` fires |
| User switches files | Current buffer retained; new file's buffer seeded from `files[path]` |
| `files[path]` changes externally while clean | Buffer pruned, falls back to saved |
| `files[path]` changes externally while dirty | Buffer preserved (user work wins) |
| `ref.resetBuffer(path)` | Buffer removed; reverts to saved |

---

## File Structure

```
src/
  index.ts                       # public exports
  types.ts                       # all interfaces
  MdExplorer.tsx                 # composes sidebar + editor + CSS injection
  MdExplorer.stories.tsx
  hooks/
    useFileBuffers.ts            # controlled/uncontrolled buffer map + dirty tracking
  sidebar/
    Sidebar.tsx                  # file rows + ToC, add-file button, kebab triggering
    KebabMenu.tsx                # floating menu anchored to the kebab button
    RenameInput.tsx              # inline rename input with collision validation
    DeleteConfirm.tsx            # inline confirm popover
  editor/
    ActiveFileEditor.tsx         # MdEditor wrapper, uncontrolled, forwards ref
  toc/
    parseToc.ts                  # markdown -> TocEntry[]
    scrollToHeading.ts           # Editor + index -> scrollIntoView
  styles/
    fileExplorerStyles.ts        # injected CSS
```

---

## Storybook Stories

| Story | Purpose |
|-------|---------|
| Default | A few files with a long plan document; active file shows pill + badge + ToC |
| Controlled | `files`/`activePath` wired to external state, rename/delete/add persisted externally |
| TocNavigation | Single long doc; clicking ToC entries scrolls the editor |
| ReadOnlyActions | No rename/delete/add callbacks — purely navigational |
| WithAddFile | Demonstrates "+" button and Untitled N.md generation |
| RenameAndDelete | Kebab → Rename / Delete with collision rejection |
| EmptyState | Custom empty state + "+" to create the first file |
| LiveTocUpdate | Demonstrates live ToC update as headings are edited |

---

## Non-Functional Requirements

| Requirement | Target |
|-------------|--------|
| Bundle size (own code, excluding @asdlc/md-editor) | < 20KB minified |
| Accessibility | `role="tree"` / `role="treeitem"` / `aria-selected`; menu items have `role="menuitem"`; kebab and add-file buttons have `aria-label` |
| Browser support | Chrome, Firefox, Safari, Edge (latest 2 versions) |

## Out of Scope

- Folders / nested file hierarchy (we explicitly removed this)
- Multi-tab / multi-pane editing
- Drag-to-reorder files
- Non-`.md` file types
- Filesystem I/O — consumer wires persistence via callbacks
- Active-heading highlighting as the user scrolls (could be added later via IntersectionObserver)
