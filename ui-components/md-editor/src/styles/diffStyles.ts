/** CSS styles for diff marks and decorations in the TipTap editor. */
export const diffContentStyles: Record<string, Record<string, string>> = {
  // Mark-based styles (MdDiffViewer)
  '.tiptap ins.diff-added': {
    backgroundColor: 'rgba(34, 139, 34, 0.15)',
    color: '#22633a',
    textDecoration: 'none',
  },
  '.tiptap del.diff-removed': {
    backgroundColor: 'rgba(220, 38, 38, 0.15)',
    color: '#991b1b',
    textDecoration: 'line-through',
  },
  // Decoration-based styles (inline diff mode)
  '.tiptap .diff-added': {
    backgroundColor: 'rgba(34, 139, 34, 0.15)',
    borderRadius: '2px',
  },
  '.tiptap .diff-removed-widget': {
    backgroundColor: 'rgba(220, 38, 38, 0.15)',
    color: '#991b1b',
    textDecoration: 'line-through',
    userSelect: 'none',
    pointerEvents: 'none',
    borderRadius: '2px',
  },
};

/** Convert diff style object to CSS string. */
export function diffStylesToCss(): string {
  const lines: string[] = [];
  for (const [selector, props] of Object.entries(diffContentStyles)) {
    lines.push(`${selector} {`);
    for (const [prop, val] of Object.entries(props)) {
      const kebab = prop.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`);
      lines.push(`  ${kebab}: ${val};`);
    }
    lines.push('}');
  }
  return lines.join('\n');
}
