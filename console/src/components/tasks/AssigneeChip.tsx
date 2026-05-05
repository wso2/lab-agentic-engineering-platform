import { Chip, useTheme } from '@wso2/oxygen-ui';
import { Bot, User } from 'lucide-react';

interface AssigneeChipProps {
  assignee?: string;
}

export function AssigneeChip({ assignee }: AssigneeChipProps) {
  const theme = useTheme();
  const sx = {
    height: 20,
    fontSize: '0.65rem',
    fontWeight: 600,
    bgcolor: `${theme.palette.primary.main}22`,
    borderColor: `${theme.palette.primary.main}55`,
    color: theme.palette.primary.main,
    border: '1px solid',
    '& .MuiChip-label': { px: 0.75 },
    '& .MuiChip-icon': { ml: 0.5, color: 'inherit' },
  };

  return assignee ? (
    <Chip icon={<User size={10} />} label={assignee} size="small" sx={sx} />
  ) : (
    <Chip icon={<Bot size={10} />} label="Automated" size="small" sx={sx} />
  );
}
