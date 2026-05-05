import { useEffect, useRef, useState } from 'react';
import type { Editor } from '@tiptap/react';
import { Box, Button, Popover, Stack, TextField } from '@wso2/oxygen-ui';

export interface LinkPopoverProps {
  editor: Editor;
  anchorEl: HTMLElement | null;
  open: boolean;
  onClose: () => void;
}

export function LinkPopover({ editor, anchorEl, open, onClose }: LinkPopoverProps) {
  const existingUrl: string = editor.getAttributes('link').href ?? '';
  const [url, setUrl] = useState(existingUrl);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setUrl(existingUrl);
      // Focus after Popover mounts its content.
      requestAnimationFrame(() => {
        inputRef.current?.focus();
        inputRef.current?.select();
      });
    }
  }, [open, existingUrl]);

  const applyLink = () => {
    if (url.trim()) {
      editor.chain().focus().extendMarkRange('link').setLink({ href: url.trim() }).run();
    } else {
      editor.chain().focus().extendMarkRange('link').unsetLink().run();
    }
    onClose();
  };

  const removeLink = () => {
    editor.chain().focus().extendMarkRange('link').unsetLink().run();
    onClose();
  };

  return (
    <Popover
      open={open}
      anchorEl={anchorEl}
      onClose={onClose}
      anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
      transformOrigin={{ vertical: 'top', horizontal: 'left' }}
    >
      <Box sx={{ p: 1.5 }} onMouseDown={(e) => e.preventDefault()}>
        <Stack direction="row" spacing={1} alignItems="center">
          <TextField
            inputRef={inputRef}
            type="url"
            placeholder="https://..."
            value={url}
            size="small"
            onChange={(e) => setUrl(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                applyLink();
              }
              if (e.key === 'Escape') {
                e.preventDefault();
                onClose();
              }
            }}
            sx={{ width: 240 }}
          />
          <Button variant="contained" size="small" onClick={applyLink}>
            Apply
          </Button>
          {existingUrl && (
            <Button variant="text" size="small" color="error" onClick={removeLink}>
              Remove
            </Button>
          )}
        </Stack>
      </Box>
    </Popover>
  );
}
