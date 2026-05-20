# MdEditor Component Specification

## Overview

A WYSIWYG markdown editor React component that provides a Google Docs-like editing experience. Users interact with rendered rich text — headings, bold, lists, code blocks, etc. — while the component's value is a standard markdown string. There is no separate edit/preview toggle; formatting is applied inline as the user types or clicks toolbar buttons.

### Design Principles

1. **Markdown in, markdown out** — the component accepts and emits CommonMark markdown strings
2. **What you see is what you get** — the editing surface shows rendered output, not raw syntax
3. **Familiar UX** — toolbar + keyboard shortcuts match Google Docs / Notion conventions
4. **Headless styling** — reads MUI/emotion theme tokens when available, falls back to sensible defaults
5. **Composable** — toolbar groups can be shown/hidden, the component can be controlled or uncontrolled

### Technology

- **Editor**: [TipTap v3](https://tiptap.dev) (ProseMirror-based headless editor) with `@tiptap/markdown` for bidirectional markdown serialization
- **Diff**: `diff` npm package for word-level text diffing
- **Storybook**: Storybook 10 with `@storybook/react-vite` for component development

### Dependencies

```
@tiptap/react, @tiptap/pm, @tiptap/starter-kit, @tiptap/markdown,
@tiptap/extension-link, @tiptap/extension-placeholder, diff
```

Peer: `react ^19.0.0`, `react-dom ^19.0.0`

---

## Component API

### MdEditor Props

```typescript
interface MdEditorProps {
  /** Controlled markdown string value */
  value?: string;

  /** Initial value for uncontrolled mode */
  defaultValue?: string;

  /** Called with markdown string on every content change */
  onChange?: (markdown: string) => void;

  /** Called on blur with final markdown string */
  onBlur?: (markdown: string) => void;

  /** Placeholder text when editor is empty */
  placeholder?: string;

  /** Disable editing */
  readOnly?: boolean;

  /** Minimum height in pixels. Default: 200 */
  minHeight?: number;

  /** Maximum height in pixels (scrolls beyond). Default: none */
  maxHeight?: number;

  /** Show/hide the toolbar. Default: true */
  showToolbar?: boolean;

  /** Which toolbar groups to display. Default: all groups */
  toolbarGroups?: ToolbarGroup[];

  /** Additional CSS class for the root container */
  className?: string;

  /** Auto-focus the editor on mount. Default: false */
  autoFocus?: boolean;

  /** Ref for imperative access to the editor */
  editorRef?: React.Ref<MdEditorRef>;

  /** Base markdown to diff against. When set, shows inline diff decorations while editing. */
  baseMarkdown?: string;
}
```

### Imperative Ref

```typescript
interface MdEditorRef {
  /** Get the current markdown content */
  getMarkdown(): string;

  /** Set markdown content programmatically */
  setMarkdown(md: string): void;

  /** Focus the editor */
  focus(): void;

  /** Access the underlying TipTap editor instance (escape hatch) */
  editor: Editor | null;
}
```

### Toolbar Groups

```typescript
type ToolbarGroup =
  | 'text-style'   // bold, italic, strikethrough, inline code
  | 'headings'     // H1, H2, H3
  | 'lists'        // bullet list, ordered list
  | 'blocks'       // blockquote, code block, horizontal rule
  | 'links'        // insert/edit link
  | 'history';     // undo, redo
```

### Usage Examples

**Uncontrolled:**
```tsx
<MdEditor defaultValue="# Hello" onChange={(md) => console.log(md)} />
```

**Controlled:**
```tsx
const [markdown, setMarkdown] = useState('');
<MdEditor value={markdown} onChange={setMarkdown} />
```

**Read-only display:**
```tsx
<MdEditor value={content} readOnly />
```

**Minimal toolbar:**
```tsx
<MdEditor toolbarGroups={['text-style', 'lists']} />
```

**Inline diff mode (show changes while editing):**
```tsx
<MdEditor value={content} onChange={setContent} baseMarkdown={originalContent} />
```

---

## Toolbar Specification

The toolbar is a horizontal bar at the top of the editor. Button groups are separated by vertical dividers. Each button shows an active/highlighted state when the cursor is inside the corresponding formatted region.

| Group | Button | Icon | Action | Keyboard Shortcut |
|-------|--------|------|--------|-------------------|
| text-style | Bold | **B** | Toggle bold | Ctrl+B |
| text-style | Italic | *I* | Toggle italic | Ctrl+I |
| text-style | Strikethrough | ~~S~~ | Toggle strikethrough | Ctrl+Shift+X |
| text-style | Code | `<>` | Toggle inline code | Ctrl+E |
| headings | Heading 1 | H1 | Toggle heading level 1 | Ctrl+Alt+1 |
| headings | Heading 2 | H2 | Toggle heading level 2 | Ctrl+Alt+2 |
| headings | Heading 3 | H3 | Toggle heading level 3 | Ctrl+Alt+3 |
| lists | Bullet List | • | Toggle bullet list | Ctrl+Shift+8 |
| lists | Ordered List | 1. | Toggle ordered list | Ctrl+Shift+7 |
| blocks | Blockquote | " | Toggle blockquote | Ctrl+Shift+B |
| blocks | Code Block | ``` | Toggle code block | Ctrl+Alt+C |
| blocks | Horizontal Rule | — | Insert horizontal rule | — |
| links | Link | chain icon | Open link URL popover | Ctrl+K |
| history | Undo | arrow icon | Undo last action | Ctrl+Z |
| history | Redo | arrow icon | Redo last action | Ctrl+Shift+Z |

### Link Popover

When the user clicks the link button or presses Ctrl+K:
- If text is selected and is not a link: show a small popover with a URL input field
- If the cursor is inside an existing link: show the popover pre-filled with the current URL, with an option to remove the link
- Pressing Enter in the URL field applies the link and closes the popover
- Pressing Escape cancels

---

## Markdown Support Matrix

| Feature | Markdown Syntax | Toolbar | Shortcut | Roundtrip |
|---------|----------------|---------|----------|-----------|
| Bold | `**text**` | Yes | Ctrl+B | Yes |
| Italic | `*text*` | Yes | Ctrl+I | Yes |
| Strikethrough | `~~text~~` | Yes | Ctrl+Shift+X | Yes |
| Inline code | `` `code` `` | Yes | Ctrl+E | Yes |
| Heading 1 | `# text` | Yes | Ctrl+Alt+1 | Yes |
| Heading 2 | `## text` | Yes | Ctrl+Alt+2 | Yes |
| Heading 3 | `### text` | Yes | Ctrl+Alt+3 | Yes |
| Bullet list | `- item` | Yes | Ctrl+Shift+8 | Yes |
| Ordered list | `1. item` | Yes | Ctrl+Shift+7 | Yes |
| Blockquote | `> text` | Yes | Ctrl+Shift+B | Yes |
| Code block | ` ``` code ``` ` | Yes | Ctrl+Alt+C | Yes |
| Horizontal rule | `---` | Yes | — | Yes |
| Link | `[text](url)` | Yes | Ctrl+K | Yes |
| Hard break | Shift+Enter | — | Shift+Enter | Yes |
| Paragraph | blank line | — | Enter | Yes |

### Not Supported (Future Work)

- Images / file upload
- Tables
- Task lists (checkboxes)
- Footnotes
- Math / LaTeX

---

## Theming

The component reads MUI/emotion theme tokens from the React context via `useTheme()`.

### Tokens Used

| Token | Usage | Fallback |
|-------|-------|----------|
| `palette.text.primary` | Editor body text | `#1a1a1a` |
| `palette.text.secondary` | Placeholder text | `#999` |
| `palette.divider` | Border, toolbar dividers | `#e0e0e0` |
| `palette.action.hover` | Toolbar button hover | `#f5f5f5` |
| `palette.action.selected` | Active toolbar button | `#e8e8e8` |
| `palette.primary.main` | Focus border, link color | `#1976d2` |
| `palette.background.paper` | Editor background | `#fff` |
| `typography.fontFamily` | Editor body font | system sans-serif |
| `typography.fontSize` | Base font size | `14px` |
| `shape.borderRadius` | Container border radius | `4px` |

When no MUI theme provider is present, all values fall back to the defaults listed above.

Dark mode is supported automatically when the MUI theme palette mode is `dark`.

---

## Architecture

### Data Flow

```
markdown string (value prop)
  -> TipTap document (ProseMirror)
  -> rendered rich text (contenteditable)
  -> user edits
  -> TipTap document updates
  -> @tiptap/markdown serializes to markdown string
  -> onChange callback
```

### File Structure

```
src/
  index.ts                          # Public exports
  types.ts                          # All TypeScript interfaces and types
  MdEditor.tsx                      # Main WYSIWYG editor component
  MdDiffViewer.tsx                  # Read-only diff viewer component
  MdEditor.stories.tsx              # Editor Storybook stories
  MdDiffViewer.stories.tsx          # Diff viewer Storybook stories
  hooks/
    useMarkdownEditor.ts            # TipTap useEditor + extensions + markdown config
    useControlledEditor.ts          # Controlled/uncontrolled value synchronization
    useEditorStorage.ts             # Snapshot-based localStorage persistence + undo/redo
  extensions/
    index.ts                        # createExtensions() factory — assembles TipTap extensions
    diffDecorations.ts              # ProseMirror Plugin for inline diff decorations
  diff/
    computeDiff.ts                  # Structural diff: two JSONContent docs -> merged doc with marks
    computeDecorations.ts           # Diff -> ProseMirror Decoration objects for inline mode
    diffMarks.ts                    # TipTap Mark definitions (DiffAdded, DiffRemoved)
  toolbar/
    Toolbar.tsx                     # Toolbar container with grouped buttons
    ToolbarButton.tsx               # Individual button with active state and hover
    ToolbarDivider.tsx              # Vertical separator between groups
    LinkPopover.tsx                 # URL input popover for link insertion/editing
  styles/
    editorStyles.ts                 # CSS for editor content (headings, lists, code, etc.)
    diffStyles.ts                   # CSS for diff marks and decorations
```

### Controlled Value Sync

The most critical implementation detail. To avoid cursor jumps in controlled mode:

1. Track the last markdown string emitted via `onChange` in a ref (`lastEmittedRef`)
2. When `value` prop changes:
   - If `value === lastEmittedRef.current` -> skip (this is an echo from our own onChange)
   - If `value !== lastEmittedRef.current` -> external update, call `editor.commands.setContent()` with `{ contentType: 'markdown' }`
3. On every TipTap `update` event:
   - Serialize document to markdown
   - Store in `lastEmittedRef.current`
   - Call `onChange(markdown)`

### Extension Assembly (`createExtensions`)

The `createExtensions()` factory in `extensions/index.ts` assembles the TipTap extension set:

```typescript
function createExtensions(options: {
  placeholder?: string;
  includeDiffMarks?: boolean;  // for MdDiffViewer (mark-based diff)
  baseMarkdown?: string;       // for MdEditor inline diff (decoration-based)
}): Extensions
```

Extensions included:
- **StarterKit** — bold, italic, strike, headings, lists, blockquote, codeBlock, horizontalRule, history, etc.
- **Link** — `openOnClick: false`, `target: _blank`, `rel: noopener noreferrer`
- **Placeholder** — configurable placeholder text
- **Markdown** — bidirectional markdown serialization via `@tiptap/markdown`
- **DiffAdded/DiffRemoved marks** (optional) — for MdDiffViewer's mark-based rendering
- **DiffDecorations** (optional) — ProseMirror Plugin for MdEditor's inline diff mode

---

## Inline Diff Mode

When `baseMarkdown` is provided to `MdEditor`, the editor shows live diff decorations while the user types. Additions are highlighted in green; deleted text appears as non-editable red strikethrough inline widgets.

### How It Works

Uses **ProseMirror decorations** (ephemeral visual overlays), not document marks. This means:
- `getMarkdown()` returns clean content — no diff artifacts
- Editing, undo/redo, copy/paste all work normally
- Decorations recompute on every document change

### Architecture

```
baseMarkdown -> parse to text blocks (cached)
                                                  -> computeDecorations() -> DecorationSet
current ProseMirror doc -> collect text blocks ---/
```

1. The `DiffDecorations` extension creates a ProseMirror Plugin
2. On init, it parses `baseMarkdown` into text block strings via `extractBaseTexts()`
3. On every transaction where `docChanged`, it:
   - Collects text blocks from the current ProseMirror document with their positions
   - Aligns current blocks against base blocks using text fingerprints
   - Runs `diffWords()` on each matched pair
   - Creates `Decoration.inline()` for added text (green highlight)
   - Creates `Decoration.widget()` for deleted text (red strikethrough, non-editable `<del>` element)
4. Returns a `DecorationSet` which ProseMirror renders as visual overlays

### Styling

```css
.tiptap .diff-added {
  background-color: rgba(34, 139, 34, 0.15);
  border-radius: 2px;
}
.tiptap .diff-removed-widget {
  background-color: rgba(220, 38, 38, 0.15);
  color: #991b1b;
  text-decoration: line-through;
  user-select: none;
  pointer-events: none;
  border-radius: 2px;
}
```

### Usage

```tsx
const [content, setContent] = useState(originalMarkdown);

<MdEditor
  value={content}
  onChange={setContent}
  baseMarkdown={originalMarkdown}
/>
```

---

## Diff Viewer (`MdDiffViewer`)

A separate read-only component that visualizes differences between two markdown documents. Uses TipTap marks (not decorations) to render a merged document.

### Component API

```typescript
interface MdDiffViewerProps {
  /** The original markdown content (before changes) */
  oldMarkdown: string;
  /** The updated markdown content (after changes) */
  newMarkdown: string;
  /** Minimum height in pixels. Default: 200 */
  minHeight?: number;
  /** Maximum height in pixels (scrolls beyond). Default: none */
  maxHeight?: number;
  /** Additional CSS class for the root container */
  className?: string;
}
```

### Usage

```tsx
<MdDiffViewer
  oldMarkdown="# Hello\n\nThis is the original text."
  newMarkdown="# Hello World\n\nThis is the updated text."
/>
```

### Architecture

```
oldMarkdown -> parse to JSONContent -\
                                      -> computeDiffDocument() -> merged JSONContent -> TipTap (read-only)
newMarkdown -> parse to JSONContent -/
```

The `computeDiffDocument()` algorithm:

1. Extract block-level children from both documents
2. Align blocks using text fingerprints (handles inserted/deleted blocks)
3. For matched blocks with different content: run `diffWords()` on text, rebuild inline content with `diffAdded`/`diffRemoved` marks
4. For container nodes (lists, blockquotes): recurse into children
5. Unmatched old blocks -> all text gets `diffRemoved` mark
6. Unmatched new blocks -> all text gets `diffAdded` mark
7. Return merged `JSONContent` document

### Rendering

| Change Type | Visual Treatment |
|-------------|-----------------|
| Deleted text | Red text with strikethrough (`<del class="diff-removed">`) |
| Added text | Green text (`<ins class="diff-added">`) |
| Unchanged text | Normal rendering |
| Deleted block | Entire block shown with red strikethrough |
| Added block | Entire block shown in green |
| Modified block | Inline word-level diff within the block |

---

## Persistence (`useEditorStorage` hook)

A standalone React hook that persists editor content to localStorage using a snapshot-based approach. Enables undo/redo across browser sessions.

### API

```typescript
interface UseEditorStorageOptions {
  storageKey: string;       // localStorage key
  maxSnapshots?: number;    // max history depth (default: 50)
  debounceMs?: number;      // save debounce delay (default: 500ms)
}

interface UseEditorStorageReturn {
  value: string;            // current markdown (latest snapshot or '')
  onChange: (md: string) => void;  // call on every editor change
  undo: () => void;
  redo: () => void;
  canUndo: boolean;
  canRedo: boolean;
  clear: () => void;        // wipe all saved snapshots
}
```

### Usage

```tsx
const storage = useEditorStorage({ storageKey: 'doc-123' });

<MdEditor value={storage.value} onChange={storage.onChange} />

// Persistent undo/redo buttons
<button onClick={storage.undo} disabled={!storage.canUndo}>Undo</button>
<button onClick={storage.redo} disabled={!storage.canRedo}>Redo</button>
```

### How It Works

1. **On mount**: loads `{ snapshots, pointer }` from `localStorage[storageKey]`
2. **On change** (debounced): truncates redo history, pushes new markdown snapshot, caps at `maxSnapshots`
3. **Undo/redo**: moves pointer through snapshot array, returns `snapshots[pointer]` as `value`
4. **On unmount**: flushes any pending debounced save

### localStorage Format

```json
{ "snapshots": ["# v1...", "# v2...", "# v3..."], "pointer": 2 }
```

---

## Non-Functional Requirements

| Requirement | Target |
|-------------|--------|
| Bundle size (all TipTap deps) | < 150KB gzipped |
| Time to interactive | < 100ms for typical documents |
| Accessibility | WCAG 2.1 AA — keyboard navigation, ARIA labels, focus management |
| Browser support | Chrome, Firefox, Safari, Edge (latest 2 versions) |
| Markdown roundtrip fidelity | All supported syntax roundtrips without content loss |

---

## Testing Strategy

### Storybook Stories

**MdEditor stories:**

| Story | Purpose |
|-------|---------|
| Default | Empty uncontrolled editor |
| WithInitialContent | Pre-populated with realistic markdown |
| Controlled | value/onChange with raw markdown output panel |
| ReadOnly | Non-editable rendered content |
| NoToolbar | `showToolbar={false}`, keyboard shortcuts only |
| CustomToolbarGroups | Subset of toolbar groups |
| MarkdownRoundtrip | Side-by-side raw textarea + WYSIWYG for bidirectional sync |
| WithPlaceholder | Custom placeholder text |
| WithPersistence | `useEditorStorage` demo with undo/redo/clear buttons |

**MdDiffViewer stories:**

| Story | Purpose |
|-------|---------|
| BasicDiff | Word edits within paragraphs |
| AddedContent | New paragraphs/blocks added |
| RemovedContent | Paragraphs/blocks deleted |
| MixedChanges | Additions, removals, and modifications |
| NoChanges | Identical content (no diff marks) |
| InteractiveDiff | MdEditor with `baseMarkdown` — inline diff while editing |

### Future: Automated Tests

- Unit: markdown serialization roundtrip for each supported syntax
- Unit: diff algorithm correctness (computeDiffDocument, computeDecorations)
- Interaction: Storybook play functions — click toolbar buttons, verify formatting
- Visual regression: Storybook snapshot comparison

---

## Open Questions / Future Work

- **Image upload** — drag-and-drop or paste images, upload to object storage, insert as `![alt](url)`
- **Tables** — GFM table editing with TipTap's table extension
- **Task lists** — checkbox lists (`- [ ] item`)
- **Slash commands** — `/` menu for quick formatting (Notion-style)
- **Collaborative editing** — TipTap has Yjs support for real-time collaboration
- **Syntax highlighting** — code block language detection and highlighting via lowlight
