import type { Theme } from '@mui/material/styles';
import { alpha } from '@mui/material/styles';
import type { StageState } from './types.js';

export interface ResolvedStateMeta {
  label: string;
  dot: string;
  pillBg: string;
  pillText: string;
}

export function resolveStateMeta(theme: Theme, state: StageState): ResolvedStateMeta {
  const isDark = theme.palette.mode === 'dark';
  switch (state) {
    case 'done':
      return {
        label: 'Complete',
        dot: theme.palette.success.main,
        pillBg: alpha(theme.palette.success.main, isDark ? 0.18 : 0.1),
        pillText: isDark ? theme.palette.success.light : theme.palette.success.dark,
      };
    case 'active':
      return {
        label: 'Running',
        dot: theme.palette.primary.main,
        pillBg: alpha(theme.palette.primary.main, isDark ? 0.2 : 0.12),
        pillText: isDark ? theme.palette.primary.light : theme.palette.primary.dark,
      };
    case 'blocked':
      return {
        label: 'Blocked',
        dot: theme.palette.error.main,
        pillBg: alpha(theme.palette.error.main, isDark ? 0.18 : 0.12),
        pillText: isDark ? theme.palette.error.light : theme.palette.error.dark,
      };
    case 'pending':
    default:
      return {
        label: 'Pending',
        dot: theme.palette.text.disabled,
        pillBg: alpha(theme.palette.text.disabled, isDark ? 0.18 : 0.12),
        pillText: theme.palette.text.secondary,
      };
  }
}
