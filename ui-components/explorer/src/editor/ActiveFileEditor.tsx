import { useEffect, useImperativeHandle, useRef } from 'react';
import { MdEditor, type MdEditorRef } from '@asdlc/md-editor';
import type { ExplorerEditorProps } from '../types.js';

interface ActiveFileEditorProps {
  /** Current markdown content. Mount-time value seeds the uncontrolled editor;
   * subsequent prop changes that did not originate from the editor itself (e.g.
   * SSE streaming updates) are pushed in via setMarkdown. */
  initialContent: string;
  onChange: (md: string) => void;
  editorProps?: ExplorerEditorProps;
  editorRef?: React.Ref<MdEditorRef>;
}

/**
 * Wraps MdEditor in uncontrolled mode so switching active files (via key remount
 * on the parent) does not trigger controlled-sync cursor jumps. Every keystroke
 * is forwarded via onChange to the explorer's buffer map, and external content
 * updates (e.g. streaming) are synced into the editor imperatively.
 */
export function ActiveFileEditor({
  initialContent,
  onChange,
  editorProps,
  editorRef,
}: ActiveFileEditorProps) {
  const innerRef = useRef<MdEditorRef>(null);
  const lastLocalChangeRef = useRef<string>(initialContent);
  // setMarkdown dispatches a Tiptap transaction that fires the editor's
  // update event synchronously. That echo must NOT round-trip back to the
  // buffer — if the editor normalises the markdown (e.g. trailing newline)
  // the buffer would diverge from saved and shadow subsequent streaming
  // updates from useFileBuffers.
  const suppressOnChangeRef = useRef(false);

  useImperativeHandle(editorRef, () => innerRef.current as MdEditorRef, []);

  // In collab mode the Y.Doc owns content. We only need to step aside when
  // edits could actually be happening — i.e., the editor is editable. While
  // readOnly (streaming, historical view) the editor is a passive sink and
  // it's safe to push prop-derived content via setMarkdown even with a
  // collab provider attached (no peer ops can be in flight against a
  // read-only client and the local user can't type).
  const collabActive =
    editorProps?.collab !== undefined && editorProps?.readOnly !== true;

  useEffect(() => {
    if (collabActive) return;
    if (initialContent === lastLocalChangeRef.current) return;
    const editor = innerRef.current;
    if (!editor) return;
    if (editor.getMarkdown() === initialContent) {
      lastLocalChangeRef.current = initialContent;
      return;
    }
    suppressOnChangeRef.current = true;
    try {
      editor.setMarkdown(initialContent);
    } finally {
      suppressOnChangeRef.current = false;
    }
    lastLocalChangeRef.current = initialContent;
  }, [initialContent, collabActive]);

  const handleChange = (md: string) => {
    if (suppressOnChangeRef.current) return;
    lastLocalChangeRef.current = md;
    onChange(md);
  };

  return (
    <MdEditor
      value={initialContent}
      onChange={handleChange}
      editorRef={innerRef}
      fillHeight
      contentMaxWidth="none"
      {...editorProps}
    />
  );
}
