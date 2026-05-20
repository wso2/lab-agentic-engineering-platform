import type React from 'react';
import { IconButton, Tooltip } from '@wso2/oxygen-ui';

export interface ToolbarButtonProps {
  label: string;
  icon: React.ReactNode;
  isActive?: boolean;
  onClick: () => void;
  disabled?: boolean;
}

export function ToolbarButton({
  label,
  icon,
  isActive = false,
  onClick,
  disabled = false,
}: ToolbarButtonProps) {
  const button = (
    <IconButton
      size="small"
      aria-label={label}
      aria-pressed={isActive}
      disabled={disabled}
      onClick={onClick}
      sx={{
        width: 30,
        height: 30,
        borderRadius: 1,
        color: isActive ? 'primary.main' : 'text.secondary',
        bgcolor: isActive ? 'color-mix(in srgb, currentColor 12%, transparent)' : 'transparent',
        '&:hover': {
          bgcolor: isActive
            ? 'color-mix(in srgb, currentColor 18%, transparent)'
            : 'action.hover',
        },
      }}
    >
      {icon}
    </IconButton>
  );

  if (disabled) return button;
  return <Tooltip title={label}>{button}</Tooltip>;
}
