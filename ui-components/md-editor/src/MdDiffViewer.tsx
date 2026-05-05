import { useEffect, useRef, useId, useMemo, useState } from 'react';
import { useEditor, EditorContent } from '@tiptap/react';
import { createExtensions } from './extensions/index.js';
import { computeDiffDocument } from './diff/computeDiff.js';
import { editorStylesToCss } from './styles/editorStyles.js';
import { diffStylesToCss } from './styles/diffStyles.js';
import type { MdDiffViewerProps } from './types.js';

type Mode = 'diff' | 'view';

const modeIcons = {
  diff: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M6 3v12" />
      <circle cx="6" cy="18" r="3" />
      <path d="M18 6V9" />
      <circle cx="18" cy="15" r="3" />
      <circle cx="6" cy="6" r="3" />
      <path d="M15 6h3a3 3 0 0 1 3 3" />
    </svg>
  ),
  view: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  ),
};

export function MdDiffViewer({
  oldMarkdown,
  newMarkdown,
  minHeight = 200,
  maxHeight,
  className,
}: MdDiffViewerProps) {
  const styleId = useId();
  const styleInjectedRef = useRef(false);
  const [mode, setMode] = useState<Mode>('diff');

  // Inject editor + diff styles once
  useEffect(() => {
    if (styleInjectedRef.current) return;
    const existing = document.getElementById(`md-diff-styles-${styleId}`);
    if (existing) return;
    const style = document.createElement('style');
    style.id = `md-diff-styles-${styleId}`;
    style.textContent = editorStylesToCss() + '\n' + diffStylesToCss();
    document.head.appendChild(style);
    styleInjectedRef.current = true;
    return () => {
      style.remove();
      styleInjectedRef.current = false;
    };
  }, [styleId]);

  const editor = useEditor({
    extensions: createExtensions({ includeDiffMarks: true }),
    editable: false,
    content: '',
  });

  // Compute diff document once per markdown pair — reused whenever user
  // switches back to diff mode.
  const diffDoc = useMemo(() => {
    if (!editor?.markdown) return null;
    const oldDoc = editor.markdown.parse(oldMarkdown);
    const newDoc = editor.markdown.parse(newMarkdown);
    return computeDiffDocument(oldDoc, newDoc);
  }, [editor?.markdown, oldMarkdown, newMarkdown]);

  useEffect(() => {
    if (!editor) return;
    if (mode === 'diff') {
      if (diffDoc) editor.commands.setContent(diffDoc);
    } else {
      editor.commands.setContent(newMarkdown, { contentType: 'markdown' });
    }
  }, [editor, mode, diffDoc, newMarkdown]);

  return (
    <div
      className={className}
      style={{
        width: '100%',
        border: '1px solid #e0e0e0',
        borderRadius: '6px',
        overflow: 'hidden',
        background: '#fff',
      }}
    >
      <ModeToolbar mode={mode} onChange={setMode} />
      <div
        style={{
          minHeight: `${minHeight}px`,
          maxHeight: maxHeight ? `${maxHeight}px` : undefined,
          overflowY: maxHeight ? 'auto' : undefined,
          cursor: 'default',
        }}
      >
        <div
          style={{
            maxWidth: '816px',
            margin: '0 auto',
            padding: '12px 16px',
          }}
        >
          <EditorContent editor={editor} />
        </div>
      </div>
    </div>
  );
}

function ModeToolbar({ mode, onChange }: { mode: Mode; onChange: (m: Mode) => void }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'flex-start',
        gap: '2px',
        padding: '4px 8px',
        borderBottom: '1px solid #e0e0e0',
        background: '#fafafa',
      }}
    >
      <ModeButton
        label="Diff"
        icon={modeIcons.diff}
        active={mode === 'diff'}
        onClick={() => onChange('diff')}
      />
      <ModeButton
        label="View"
        icon={modeIcons.view}
        active={mode === 'view'}
        onClick={() => onChange('view')}
      />
    </div>
  );
}

function ModeButton({
  label,
  icon,
  active,
  onClick,
}: {
  label: string;
  icon: React.ReactNode;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '6px',
        height: '32px',
        padding: '0 10px',
        border: 'none',
        borderRadius: '4px',
        background: active ? '#e8e8e8' : 'transparent',
        color: '#444',
        cursor: 'pointer',
        fontSize: '13px',
        fontWeight: active ? 600 : 500,
        lineHeight: 1,
        transition: 'background 0.15s',
      }}
      onMouseEnter={(e) => {
        if (!active) e.currentTarget.style.background = '#f0f0f0';
      }}
      onMouseLeave={(e) => {
        e.currentTarget.style.background = active ? '#e8e8e8' : 'transparent';
      }}
    >
      {icon}
      <span>{label}</span>
    </button>
  );
}
