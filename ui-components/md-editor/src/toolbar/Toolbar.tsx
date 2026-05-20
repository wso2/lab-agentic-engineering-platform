import { Fragment, useRef, useState } from 'react';
import type { Editor } from '@tiptap/react';
import { Box, Stack } from '@wso2/oxygen-ui';
import {
  Bold,
  Code,
  Heading1,
  Heading2,
  Heading3,
  Italic,
  Link as LinkIcon,
  List,
  ListOrdered,
  Minus,
  Quote,
  Redo2,
  SquareCode,
  Strikethrough,
  Undo2,
} from '@wso2/oxygen-ui-icons-react';
import { ToolbarButton } from './ToolbarButton.js';
import { ToolbarDivider } from './ToolbarDivider.js';
import { LinkPopover } from './LinkPopover.js';
import type { ToolbarGroup } from '../types.js';

const ICON_SIZE = 16;

interface ToolbarProps {
  editor: Editor;
  groups: ToolbarGroup[];
  rightContent?: React.ReactNode;
}

export function Toolbar({ editor, groups, rightContent }: ToolbarProps) {
  const [showLinkPopover, setShowLinkPopover] = useState(false);
  const linkAnchorRef = useRef<HTMLDivElement>(null);

  const groupRenderers: Record<ToolbarGroup, () => React.ReactNode> = {
    'text-style': () => (
      <>
        <ToolbarButton
          label="Bold (Ctrl+B)"
          icon={<Bold size={ICON_SIZE} />}
          isActive={editor.isActive('bold')}
          onClick={() => editor.chain().focus().toggleBold().run()}
        />
        <ToolbarButton
          label="Italic (Ctrl+I)"
          icon={<Italic size={ICON_SIZE} />}
          isActive={editor.isActive('italic')}
          onClick={() => editor.chain().focus().toggleItalic().run()}
        />
        <ToolbarButton
          label="Strikethrough (Ctrl+Shift+X)"
          icon={<Strikethrough size={ICON_SIZE} />}
          isActive={editor.isActive('strike')}
          onClick={() => editor.chain().focus().toggleStrike().run()}
        />
        <ToolbarButton
          label="Inline Code (Ctrl+E)"
          icon={<Code size={ICON_SIZE} />}
          isActive={editor.isActive('code')}
          onClick={() => editor.chain().focus().toggleCode().run()}
        />
      </>
    ),
    headings: () => (
      <>
        <ToolbarButton
          label="Heading 1 (Ctrl+Alt+1)"
          icon={<Heading1 size={ICON_SIZE} />}
          isActive={editor.isActive('heading', { level: 1 })}
          onClick={() => editor.chain().focus().toggleHeading({ level: 1 }).run()}
        />
        <ToolbarButton
          label="Heading 2 (Ctrl+Alt+2)"
          icon={<Heading2 size={ICON_SIZE} />}
          isActive={editor.isActive('heading', { level: 2 })}
          onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()}
        />
        <ToolbarButton
          label="Heading 3 (Ctrl+Alt+3)"
          icon={<Heading3 size={ICON_SIZE} />}
          isActive={editor.isActive('heading', { level: 3 })}
          onClick={() => editor.chain().focus().toggleHeading({ level: 3 }).run()}
        />
      </>
    ),
    lists: () => (
      <>
        <ToolbarButton
          label="Bullet List (Ctrl+Shift+8)"
          icon={<List size={ICON_SIZE} />}
          isActive={editor.isActive('bulletList')}
          onClick={() => editor.chain().focus().toggleBulletList().run()}
        />
        <ToolbarButton
          label="Ordered List (Ctrl+Shift+7)"
          icon={<ListOrdered size={ICON_SIZE} />}
          isActive={editor.isActive('orderedList')}
          onClick={() => editor.chain().focus().toggleOrderedList().run()}
        />
      </>
    ),
    blocks: () => (
      <>
        <ToolbarButton
          label="Blockquote (Ctrl+Shift+B)"
          icon={<Quote size={ICON_SIZE} />}
          isActive={editor.isActive('blockquote')}
          onClick={() => editor.chain().focus().toggleBlockquote().run()}
        />
        <ToolbarButton
          label="Code Block (Ctrl+Alt+C)"
          icon={<SquareCode size={ICON_SIZE} />}
          isActive={editor.isActive('codeBlock')}
          onClick={() => editor.chain().focus().toggleCodeBlock().run()}
        />
        <ToolbarButton
          label="Horizontal Rule"
          icon={<Minus size={ICON_SIZE} />}
          onClick={() => editor.chain().focus().setHorizontalRule().run()}
        />
      </>
    ),
    links: () => (
      <Box ref={linkAnchorRef} sx={{ position: 'relative', display: 'inline-flex' }}>
        <ToolbarButton
          label="Link (Ctrl+K)"
          icon={<LinkIcon size={ICON_SIZE} />}
          isActive={editor.isActive('link')}
          onClick={() => setShowLinkPopover((s) => !s)}
        />
        <LinkPopover
          editor={editor}
          anchorEl={linkAnchorRef.current}
          open={showLinkPopover}
          onClose={() => setShowLinkPopover(false)}
        />
      </Box>
    ),
    history: () => (
      <>
        <ToolbarButton
          label="Undo (Ctrl+Z)"
          icon={<Undo2 size={ICON_SIZE} />}
          onClick={() => editor.chain().focus().undo().run()}
          disabled={!editor.can().undo()}
        />
        <ToolbarButton
          label="Redo (Ctrl+Shift+Z)"
          icon={<Redo2 size={ICON_SIZE} />}
          onClick={() => editor.chain().focus().redo().run()}
          disabled={!editor.can().redo()}
        />
      </>
    ),
  };

  return (
    <Stack
      direction="row"
      alignItems="center"
      flexWrap="wrap"
      spacing={0.25}
      sx={{
        px: 1,
        py: 0.5,
        borderBottom: '1px solid',
        borderColor: 'divider',
        bgcolor: 'background.default',
      }}
    >
      <Stack direction="row" alignItems="center" flexWrap="wrap" spacing={0.25} sx={{ flex: 1, minWidth: 0 }}>
        {groups.map((group, i) => (
          <Fragment key={group}>
            {i > 0 && <ToolbarDivider />}
            {groupRenderers[group]()}
          </Fragment>
        ))}
      </Stack>
      {rightContent && (
        <Stack direction="row" alignItems="center" sx={{ flexShrink: 0 }}>
          {rightContent}
        </Stack>
      )}
    </Stack>
  );
}
