import { useEffect, useRef, useCallback } from 'react';
import type { Editor } from '@tiptap/react';
import type { Node } from '@tiptap/pm/model';
import { TextSelection } from '@tiptap/pm/state';

function pmPosToTextOffset(doc: Node, pmPos: number): number {
  const clamped = Math.min(Math.max(0, pmPos), doc.content.size);
  return doc.textBetween(0, clamped, '\n').length;
}

function textOffsetToPmPos(doc: Node, targetOffset: number): number {
  if (targetOffset <= 0) return 0;
  let acc = 0;
  let result = doc.content.size;
  doc.descendants((node, pos) => {
    if (acc >= targetOffset) return false;
    if (node.isText) {
      const remaining = targetOffset - acc;
      const textLen = node.text?.length ?? 0;
      if (remaining <= textLen) {
        result = pos + remaining;
        acc = targetOffset;
        return false;
      }
      acc += textLen;
    } else if (node.isBlock && acc > 0 && node.type.name !== 'doc') {
      // Block boundaries contribute a newline in textBetween('\n')
      acc += 1;
      if (acc >= targetOffset) {
        result = pos;
        return false;
      }
    }
    return true;
  });
  return Math.min(result, doc.content.size);
}

/**
 * Synchronizes a controlled `value` prop with a TipTap editor instance.
 *
 * The key challenge: when the user types, the editor fires onUpdate → we call
 * onChange(markdown) → the parent echoes the new value back via the `value` prop.
 * We must NOT re-set the editor content on that echo, or the cursor jumps.
 *
 * Solution: track the last markdown we emitted. When `value` changes, only
 * apply it if it differs from what we last emitted (i.e. it's an external update).
 */
export function useControlledEditor(
  editor: Editor | null,
  value: string | undefined,
  onChange: ((markdown: string) => void) | undefined,
) {
  const lastEmittedRef = useRef<string | null>(null);
  const isControlled = value !== undefined;

  // Wrap onUpdate to track emitted values
  const handleUpdate = useCallback(
    (markdown: string) => {
      lastEmittedRef.current = markdown;
      onChange?.(markdown);
    },
    [onChange],
  );

  // Sync external value changes into the editor
  useEffect(() => {
    if (!editor || !isControlled) return;

    // Skip if this is an echo of our own emission
    if (value === lastEmittedRef.current) return;

    // External update — apply to editor without triggering onUpdate loop.
    // Preserve cursor by round-tripping through plain-text offset so remote
    // collaborative edits don't yank the caret to the end of the doc.
    const currentMarkdown = editor.getMarkdown();
    if (value !== currentMarkdown) {
      const hadFocus = editor.isFocused;
      const { head, anchor } = editor.state.selection;
      const headOffset = pmPosToTextOffset(editor.state.doc, head);
      const anchorOffset = pmPosToTextOffset(editor.state.doc, anchor);

      editor.commands.setContent(value || '', { contentType: 'markdown', emitUpdate: false });

      const newDoc = editor.state.doc;
      const newHead = textOffsetToPmPos(newDoc, headOffset);
      const newAnchor = textOffsetToPmPos(newDoc, anchorOffset);
      const tr = editor.state.tr.setSelection(
        TextSelection.create(newDoc, newAnchor, newHead),
      );
      editor.view.dispatch(tr);
      if (hadFocus) editor.commands.focus(undefined, { scrollIntoView: false });
    }
    lastEmittedRef.current = value;
  }, [editor, value, isControlled]);

  return handleUpdate;
}
