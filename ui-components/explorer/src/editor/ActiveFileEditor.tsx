import { useEffect, useImperativeHandle, useRef } from 'react';
import { MdEditor, type MdEditorRef } from '@asdlc/md-editor';
import {
  ExcalidrawEditor,
  type ExcalidrawEditorRef,
} from '@asdlc/excalidraw-editor';
import type { ExplorerEditorProps } from '../types.js';

interface ActiveFileEditorProps {
  /** Path of the active file. Used to pick the editor by extension. */
  activePath: string;
  /** Current content. Mount-time value seeds the uncontrolled editor;
   * subsequent prop changes that did not originate from the editor itself (e.g.
   * SSE streaming updates) are pushed in via setMarkdown / setContent. */
  initialContent: string;
  onChange: (md: string) => void;
  editorProps?: ExplorerEditorProps;
  editorRef?: React.Ref<MdEditorRef>;
}

function isExcalidrawPath(path: string): boolean {
  return /\.excalidraw$/i.test(path);
}

/**
 * Wraps an editor in uncontrolled mode so switching active files (via key remount
 * on the parent) does not trigger controlled-sync cursor jumps. Every change is
 * forwarded via onChange to the explorer's buffer map, and external content
 * updates (e.g. streaming) are synced into the editor imperatively.
 *
 * The component dispatches by file extension: `.excalidraw` files use the
 * Excalidraw canvas editor; everything else uses the markdown editor.
 */
export function ActiveFileEditor({
  activePath,
  initialContent,
  onChange,
  editorProps,
  editorRef,
}: ActiveFileEditorProps) {
  if (isExcalidrawPath(activePath)) {
    return (
      <ExcalidrawFileEditor
        initialContent={initialContent}
        onChange={onChange}
        readOnly={editorProps?.readOnly}
      />
    );
  }
  return (
    <MdFileEditor
      initialContent={initialContent}
      onChange={onChange}
      editorProps={editorProps}
      editorRef={editorRef}
    />
  );
}

interface MdFileEditorProps {
  initialContent: string;
  onChange: (md: string) => void;
  editorProps?: ExplorerEditorProps;
  editorRef?: React.Ref<MdEditorRef>;
}

function MdFileEditor({
  initialContent,
  onChange,
  editorProps,
  editorRef,
}: MdFileEditorProps) {
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

interface ExcalidrawFileEditorProps {
  initialContent: string;
  onChange: (json: string) => void;
  readOnly?: boolean;
}

function ExcalidrawFileEditor({
  initialContent,
  onChange,
  readOnly,
}: ExcalidrawFileEditorProps) {
  const innerRef = useRef<ExcalidrawEditorRef>(null);
  const lastLocalChangeRef = useRef<string>(initialContent);

  useEffect(() => {
    if (initialContent === lastLocalChangeRef.current) return;
    const editor = innerRef.current;
    if (!editor) return;
    if (editor.getContent() === initialContent) {
      lastLocalChangeRef.current = initialContent;
      return;
    }
    editor.setContent(initialContent);
    lastLocalChangeRef.current = initialContent;
  }, [initialContent]);

  const handleChange = (json: string) => {
    lastLocalChangeRef.current = json;
    onChange(json);
  };

  return (
    <ExcalidrawEditor
      value={initialContent}
      onChange={handleChange}
      editorRef={innerRef}
      readOnly={readOnly}
      fillHeight
    />
  );
}
