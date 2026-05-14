import type { Editor } from '@tiptap/core';

/**
 * Scroll the Nth heading of a TipTap editor into view. The index corresponds
 * to the 0-based position among all heading nodes in the document, matching
 * {@link parseToc}.
 */
export function scrollToHeading(editor: Editor, headingIndex: number): void {
  if (headingIndex < 0) return;

  let current = 0;
  let targetPos = -1;
  editor.state.doc.descendants((node, pos) => {
    if (node.type.name === 'heading') {
      if (current === headingIndex) {
        targetPos = pos;
        return false;
      }
      current++;
    }
    return true;
  });

  if (targetPos < 0) return;

  const dom = editor.view.nodeDOM(targetPos);
  if (dom instanceof HTMLElement) {
    dom.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
}
