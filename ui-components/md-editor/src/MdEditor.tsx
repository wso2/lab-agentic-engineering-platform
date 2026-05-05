import { useEffect, useImperativeHandle, useRef, useId } from 'react';
import { EditorContent } from '@tiptap/react';
import { Box } from '@wso2/oxygen-ui';
import { useMarkdownEditor } from './hooks/useMarkdownEditor.js';
import { useControlledEditor } from './hooks/useControlledEditor.js';
import { Toolbar } from './toolbar/Toolbar.js';
import { editorStylesToCss } from './styles/editorStyles.js';
import { diffStylesToCss } from './styles/diffStyles.js';
import { diffDecorationsKey } from './extensions/diffDecorations.js';
import type { MdEditorProps } from './types.js';
import { ALL_TOOLBAR_GROUPS } from './types.js';

export function MdEditor({
  value,
  defaultValue = '',
  onChange,
  onBlur,
  placeholder = 'Write something...',
  readOnly = false,
  minHeight = 200,
  maxHeight,
  fillHeight = false,
  showToolbar = true,
  toolbarGroups = ALL_TOOLBAR_GROUPS,
  toolbarRightContent,
  className,
  autoFocus = false,
  editorRef,
  baseMarkdown,
  collab,
  contentMaxWidth = '816px',
}: MdEditorProps) {
  const styleId = useId();
  const styleInjectedRef = useRef(false);
  // Back the diff baseline with a ref so the DiffDecorations plugin (which is
  // baked into the editor at create time) can read the latest value via getter
  // without recreating the editor.
  const baseMarkdownRef = useRef<string | undefined>(baseMarkdown);
  baseMarkdownRef.current = baseMarkdown;

  // Inject editor styles once per mount
  useEffect(() => {
    if (styleInjectedRef.current) return;
    const existing = document.getElementById(`md-editor-styles-${styleId}`);
    if (existing) return;
    const style = document.createElement('style');
    style.id = `md-editor-styles-${styleId}`;
    style.textContent = editorStylesToCss() + '\n' + diffStylesToCss();
    document.head.appendChild(style);
    styleInjectedRef.current = true;
    return () => {
      style.remove();
      styleInjectedRef.current = false;
    };
  }, [styleId]);

  const initialContent = value ?? defaultValue;

  const editor = useMarkdownEditor({
    content: initialContent,
    placeholder,
    editable: !readOnly,
    autoFocus,
    onBlur,
    getBaseMarkdown: () => baseMarkdownRef.current,
    collab,
  });

  // Controlled value sync — only when NOT in collab mode. In collab mode the
  // Y.Doc owns the content; pushing `value` into the editor would clobber the
  // CRDT and round-trip through markdown re-parses on every keystroke.
  const handleUpdate = useControlledEditor(
    editor,
    collab ? undefined : value,
    onChange,
  );
  useEffect(() => {
    if (!editor) return;
    const handler = () => { handleUpdate(editor.getMarkdown()); };
    editor.on('update', handler);
    return () => { editor.off('update', handler); };
  }, [editor, handleUpdate]);

  // Sync readOnly
  useEffect(() => {
    if (editor) editor.setEditable(!readOnly, false);
  }, [editor, readOnly]);

  // When the diff baseline changes (e.g. lastTaggedContent fetched async),
  // ping the DiffDecorations plugin so it re-reads the getter and re-parses.
  useEffect(() => {
    if (!editor) return;
    editor.view.dispatch(editor.state.tr.setMeta(diffDecorationsKey, true));
  }, [editor, baseMarkdown]);

  // Imperative ref
  useImperativeHandle(
    editorRef,
    () => ({
      getMarkdown: () => editor?.getMarkdown() ?? '',
      setMarkdown: (md: string) => {
        editor?.commands.setContent(md, { contentType: 'markdown' });
      },
      focus: () => editor?.commands.focus(),
      editor: editor ?? null,
    }),
    [editor],
  );

  return (
    <Box
      className={className}
      sx={{
        width: '100%',
        border: '1px solid',
        borderColor: 'divider',
        borderRadius: 1.5,
        overflow: 'hidden',
        bgcolor: 'background.paper',
        ...(fillHeight
          ? { height: '100%', display: 'flex', flexDirection: 'column', minHeight: 0 }
          : {}),
      }}
    >
      {showToolbar && !readOnly && editor && (
        <Box sx={fillHeight ? { flexShrink: 0 } : undefined}>
          <Toolbar editor={editor} groups={toolbarGroups} rightContent={toolbarRightContent} />
        </Box>
      )}
      <Box
        sx={{
          minHeight: fillHeight ? 0 : `${minHeight}px`,
          maxHeight: fillHeight ? undefined : maxHeight ? `${maxHeight}px` : undefined,
          overflowY: fillHeight || maxHeight ? 'auto' : undefined,
          flex: fillHeight ? 1 : undefined,
          cursor: readOnly ? 'default' : 'text',
        }}
        onClick={() => { if (!readOnly) editor?.commands.focus(); }}
      >
        <Box
          sx={{
            maxWidth:
              contentMaxWidth === 'none'
                ? undefined
                : typeof contentMaxWidth === 'number'
                  ? `${contentMaxWidth}px`
                  : contentMaxWidth,
            mx: contentMaxWidth === 'none' ? undefined : 'auto',
            px: 2,
            py: 1.5,
          }}
        >
          <EditorContent editor={editor} />
        </Box>
      </Box>
    </Box>
  );
}
