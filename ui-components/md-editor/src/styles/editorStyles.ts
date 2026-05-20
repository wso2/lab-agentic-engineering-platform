/**
 * CSS for the TipTap content area. Colors use Oxygen UI's CSS variables (with
 * MUI-prefix and hardcoded fallbacks) so the editor content follows the active
 * color scheme (light/dark) when rendered inside an OxygenUIThemeProvider,
 * and still has reasonable defaults when used standalone.
 */

// Resolve a color via Oxygen -> MUI -> hardcoded fallback.
const c = (name: string, fallback: string) =>
  `var(--oxygen-palette-${name}, var(--mui-palette-${name}, ${fallback}))`;

export const editorContentStyles: Record<string, string | Record<string, string | Record<string, string>>> = {
  '.tiptap': {
    outline: 'none',
    fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    fontSize: '14px',
    lineHeight: '1.7',
    color: c('text-primary', '#1a1a1a'),
  },
  '.tiptap h1': {
    fontSize: '1.75em',
    fontWeight: '700',
    marginTop: '1.5em',
    marginBottom: '0.5em',
    lineHeight: '1.3',
  },
  '.tiptap h2': {
    fontSize: '1.4em',
    fontWeight: '600',
    marginTop: '1.25em',
    marginBottom: '0.4em',
    lineHeight: '1.3',
  },
  '.tiptap h3': {
    fontSize: '1.15em',
    fontWeight: '600',
    marginTop: '1em',
    marginBottom: '0.3em',
    lineHeight: '1.4',
  },
  '.tiptap p': {
    marginTop: '0',
    marginBottom: '0.75em',
  },
  '.tiptap ul, .tiptap ol': {
    paddingLeft: '1.5em',
    marginTop: '0',
    marginBottom: '0.75em',
  },
  '.tiptap li': {
    marginBottom: '0.25em',
  },
  '.tiptap li p': {
    marginBottom: '0',
  },
  '.tiptap blockquote': {
    borderLeft: `3px solid ${c('divider', '#d0d0d0')}`,
    marginLeft: '0',
    marginRight: '0',
    paddingLeft: '1em',
    color: c('text-secondary', '#666'),
    fontStyle: 'italic',
  },
  '.tiptap code': {
    backgroundColor: c('action-hover', '#f0f0f0'),
    color: c('text-primary', '#1a1a1a'),
    borderRadius: '3px',
    padding: '0.15em 0.35em',
    fontSize: '0.9em',
    fontFamily: '"Fira Code", "Cascadia Code", Consolas, monospace',
  },
  '.tiptap pre': {
    backgroundColor: c('action-selected', '#f5f5f5'),
    color: c('text-primary', '#1a1a1a'),
    borderRadius: '6px',
    padding: '0.75em 1em',
    overflow: 'auto',
    marginTop: '0',
    marginBottom: '0.75em',
  },
  '.tiptap pre code': {
    backgroundColor: 'transparent',
    padding: '0',
    borderRadius: '0',
    fontSize: '0.875em',
  },
  '.tiptap a': {
    color: c('primary-main', '#1976d2'),
    textDecoration: 'underline',
    cursor: 'pointer',
  },
  '.tiptap hr': {
    border: 'none',
    borderTop: `1px solid ${c('divider', '#e0e0e0')}`,
    margin: '1.5em 0',
  },
  '.tiptap p.is-editor-empty:first-child::before': {
    color: c('text-disabled', '#adb5bd'),
    content: 'attr(data-placeholder)',
    float: 'left',
    height: '0',
    pointerEvents: 'none',
  },
};

// CollaborationCaret extension creates DOM elements with these classes for
// each remote peer's caret + name label. Styled to match Google-Docs vibe.
const COLLAB_CARET_CSS = `
.collaboration-carets__caret {
  position: relative;
  display: inline-block;
  width: 0;
  height: 1em;
  border-left: 2px solid;
  margin-left: -1px;
  pointer-events: none;
  user-select: none;
  vertical-align: text-bottom;
  word-break: normal;
}
.collaboration-carets__label {
  position: absolute;
  bottom: 100%;
  left: -1px;
  color: #fff;
  font-size: 10px;
  line-height: 1.5;
  padding: 0 5px;
  border-radius: 3px 3px 3px 0;
  white-space: nowrap;
  pointer-events: none;
  user-select: none;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  font-weight: 500;
  letter-spacing: 0.01em;
  z-index: 20;
}
.collaboration-carets__selection {
  border-radius: 2px;
}
`;

/** Convert the style object into a CSS string for injection via <style> tag. */
export function editorStylesToCss(): string {
  const lines: string[] = [];
  for (const [selector, props] of Object.entries(editorContentStyles)) {
    if (typeof props === 'string') continue;
    lines.push(`${selector} {`);
    for (const [prop, val] of Object.entries(props)) {
      if (typeof val === 'string') {
        const kebab = prop.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`);
        lines.push(`  ${kebab}: ${val};`);
      }
    }
    lines.push('}');
  }
  lines.push(COLLAB_CARET_CSS);
  return lines.join('\n');
}
