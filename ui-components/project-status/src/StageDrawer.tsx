import { Drawer, Box, Stack, Typography, IconButton, Chip, Divider } from '@mui/material';
import { useTheme, alpha } from '@mui/material/styles';
import { resolveStateMeta } from './stateMeta.js';
import type { StageDrawerProps } from './types.js';

export function StageDrawer({ stage, open, onClose, mode }: StageDrawerProps) {
  const theme = useTheme();
  const effectiveMode = mode ?? theme.palette.mode;
  const isDark = effectiveMode === 'dark';

  if (!stage) return null;

  const meta = resolveStateMeta(theme, stage.state);
  const muted = theme.palette.text.secondary;
  const chipBg = alpha(theme.palette.text.primary, isDark ? 0.06 : 0.04);

  const changes = stage.changes ?? [];
  const metrics = stage.metrics ?? [];
  const artifacts = stage.artifacts ?? [];

  return (
    <Drawer
      anchor="right"
      open={open}
      onClose={onClose}
      slotProps={{
        paper: {
          sx: {
            width: 420,
            maxWidth: '100vw',
            backgroundColor: theme.palette.background.paper,
            color: theme.palette.text.primary,
          },
        },
      }}
    >
      <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        {/* Header */}
        <Box
          sx={{
            px: 3,
            pt: 2.5,
            pb: 2.25,
            borderBottom: `1px solid ${theme.palette.divider}`,
            display: 'flex',
            alignItems: 'flex-start',
            gap: 1.5,
          }}
        >
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 0.75 }}>
              <Box
                sx={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  backgroundColor: meta.dot,
                  boxShadow:
                    stage.state === 'active' ? `0 0 0 4px ${meta.pillBg}` : 'none',
                }}
              />
              <Typography
                component="span"
                sx={{
                  fontSize: 11,
                  fontWeight: 600,
                  color: meta.pillText,
                  textTransform: 'uppercase',
                  letterSpacing: 0.6,
                }}
              >
                {meta.label}
              </Typography>
            </Stack>
            <Typography
              component="h2"
              sx={{ m: 0, fontSize: 22, fontWeight: 600, letterSpacing: -0.4 }}
            >
              {stage.name}
            </Typography>
            <Typography sx={{ mt: 0.5, fontSize: 13, color: muted }}>
              v{stage.iteration}
              {stage.timestamp ? ` · ${stage.timestamp}` : ''}
            </Typography>
          </Box>
          <IconButton
            onClick={onClose}
            size="small"
            aria-label="Close stage details"
            sx={{ color: muted, fontSize: 22, lineHeight: 1 }}
          >
            ×
          </IconButton>
        </Box>

        {/* Body */}
        <Box sx={{ flex: 1, overflowY: 'auto', px: 3, pt: 2.5, pb: 3.5 }}>
          {stage.summary && (
            <Typography
              sx={{
                fontSize: 14.5,
                lineHeight: 1.55,
                mb: 2.75,
                color: theme.palette.text.primary,
              }}
            >
              {stage.summary}
            </Typography>
          )}

          {metrics.length > 0 && (
            <Box
              sx={{
                display: 'grid',
                gridTemplateColumns: 'repeat(3, 1fr)',
                gap: 1,
                mb: 2.75,
              }}
            >
              {metrics.map((m) => (
                <Box
                  key={m.key}
                  sx={{ backgroundColor: chipBg, borderRadius: 1, px: 1.5, py: 1.25 }}
                >
                  <Typography
                    sx={{ fontSize: 18, fontWeight: 600, letterSpacing: -0.3 }}
                  >
                    {m.value}
                  </Typography>
                  <Typography
                    sx={{
                      fontSize: 11,
                      color: muted,
                      mt: 0.25,
                      textTransform: 'uppercase',
                      letterSpacing: 0.4,
                    }}
                  >
                    {m.key}
                  </Typography>
                </Box>
              ))}
            </Box>
          )}

          {changes.length > 0 && (
            <Box sx={{ mb: 2.75 }}>
              <Typography
                sx={{
                  fontSize: 11,
                  fontWeight: 600,
                  color: muted,
                  textTransform: 'uppercase',
                  letterSpacing: 0.6,
                  mb: 1.25,
                }}
              >
                {stage.state === 'active' ? 'Activity' : `Changes in v${stage.iteration}`}
              </Typography>
              <Stack component="ul" spacing={1} sx={{ m: 0, p: 0, listStyle: 'none' }}>
                {changes.map((c, i) => (
                  <Box
                    key={i}
                    component="li"
                    sx={{ display: 'flex', gap: 1.25, fontSize: 13.5, lineHeight: 1.5 }}
                  >
                    <Box component="span" sx={{ color: meta.pillText, mt: '1px' }}>
                      ›
                    </Box>
                    <Box component="span">{c}</Box>
                  </Box>
                ))}
              </Stack>
            </Box>
          )}

          {artifacts.length > 0 && (
            <Box sx={{ mb: 2.75 }}>
              <Typography
                sx={{
                  fontSize: 11,
                  fontWeight: 600,
                  color: muted,
                  textTransform: 'uppercase',
                  letterSpacing: 0.6,
                  mb: 1.25,
                }}
              >
                Artifacts
              </Typography>
              <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75 }}>
                {artifacts.map((a) => (
                  <Chip
                    key={a}
                    label={a}
                    size="small"
                    sx={{
                      fontSize: 12,
                      height: 24,
                      backgroundColor: chipBg,
                      color: theme.palette.text.primary,
                      borderRadius: 0.75,
                    }}
                  />
                ))}
              </Box>
            </Box>
          )}

          {(stage.duration || stage.iteration > 0) && (
            <>
              <Divider sx={{ mb: 2 }} />
              <Stack
                direction="row"
                justifyContent="space-between"
                sx={{ fontSize: 12, color: muted }}
              >
                <Box component="span">Duration · {stage.duration ?? '—'}</Box>
                <Box component="span">Iteration v{stage.iteration}</Box>
              </Stack>
            </>
          )}
        </Box>
      </Box>
    </Drawer>
  );
}
