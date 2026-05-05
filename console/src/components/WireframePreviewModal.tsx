import { useCallback, useEffect, useState } from 'react';
import {
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogContent,
  DialogTitle,
  IconButton,
  Stack,
  Typography,
} from '@wso2/oxygen-ui';
import { X } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';

interface WireframePreviewModalProps {
  open: boolean;
  onClose: () => void;
  orgHandle: string;
  projectId: string;
}

type Status = 'loading' | 'idle' | 'generating' | 'ready' | 'error';

export default function WireframePreviewModal({
  open,
  onClose,
  orgHandle,
  projectId,
}: WireframePreviewModalProps) {
  const [status, setStatus] = useState<Status>('loading');
  const [content, setContent] = useState('');
  const [error, setError] = useState<string | null>(null);

  // On open — check whether a wireframe already exists (or is being generated).
  useEffect(() => {
    if (!open || !projectId) return;

    setStatus('loading');
    setContent('');
    setError(null);

    api.getSpecWireframe(orgHandle, projectId).then((result) => {
      if (result.status === 'ready') {
        setContent(result.content);
        setStatus('ready');
      } else if (result.status === 'generating') {
        setStatus('generating'); // polling effect will kick in
      } else if (result.status === 'error') {
        setError(result.error);
        setStatus('error');
      } else {
        setStatus('idle');
      }
    });
  }, [open, orgHandle, projectId]);

  // Poll GET /wireframe every 3 s while generating.
  useEffect(() => {
    if (status !== 'generating' || !open) return;

    const id = setInterval(async () => {
      const result = await api.getSpecWireframe(orgHandle, projectId);
      if (result.status === 'ready') {
        setContent(result.content);
        setStatus('ready');
      } else if (result.status === 'error') {
        setError(result.error);
        setStatus('error');
      }
      // 'generating' or 'not_generated' → keep polling
    }, 3000);

    return () => clearInterval(id);
  }, [status, open, orgHandle, projectId]);

  const handleGenerate = useCallback(async () => {
    setStatus('generating');
    setContent('');
    setError(null);

    try {
      await api.generateSpecWireframe(orgHandle, projectId);
      // 202 received — polling effect takes over
    } catch (err) {
      setError((err as Error)?.message ?? 'Failed to start wireframe generation');
      setStatus('error');
    }
  }, [orgHandle, projectId]);

  return (
    <Dialog
      open={open}
      onClose={onClose}
      fullScreen
      PaperProps={{
        sx: {
          bgcolor: 'background.paper',
          opacity: 1,
        },
      }}
    >
      <DialogTitle sx={{ p: 2, pb: 1.5 }}>
        <Stack direction="row" alignItems="center" justifyContent="space-between">
          <Typography variant="h6" fontWeight={600}>
            Product Wireframe
          </Typography>
          <Stack direction="row" alignItems="center" gap={1}>
            {status === 'ready' && (
              <Button size="small" variant="outlined" onClick={handleGenerate}>
                Regenerate
              </Button>
            )}
            <IconButton size="small" onClick={onClose}>
              <X size={18} />
            </IconButton>
          </Stack>
        </Stack>
      </DialogTitle>

      <DialogContent sx={{ p: 0, overflow: 'hidden', position: 'relative' }}>
        {status === 'loading' && (
          <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%' }}>
            <CircularProgress size={36} />
          </Box>
        )}

        {status === 'idle' && (
          <Box
            sx={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              height: '100%',
              gap: 2,
            }}
          >
            <Typography variant="body2" color="text.secondary">
              No wireframe has been generated yet for this project.
            </Typography>
            <Button variant="contained" onClick={handleGenerate}>
              Generate Wireframe
            </Button>
          </Box>
        )}

        {status === 'generating' && (
          <Box
            sx={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              height: '100%',
              gap: 2,
            }}
          >
            <CircularProgress size={36} />
            <Typography variant="body2" color="text.secondary">
              Generating wireframe…
            </Typography>
          </Box>
        )}

        {status === 'ready' && (
          <iframe
            sandbox="allow-scripts allow-forms"
            srcDoc={content}
            title="Product wireframe"
            style={{ width: '100%', height: '100%', border: 'none' }}
          />
        )}

        {status === 'error' && (
          <Box
            sx={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              height: '100%',
              gap: 2,
              px: 4,
            }}
          >
            <Typography variant="body2" color="error" textAlign="center">
              {error}
            </Typography>
            <Button variant="outlined" onClick={handleGenerate}>
              Retry
            </Button>
          </Box>
        )}
      </DialogContent>
    </Dialog>
  );
}
