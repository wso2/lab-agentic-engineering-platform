import { alpha, Chip } from '@wso2/oxygen-ui';
import { Bot, User } from '@wso2/oxygen-ui-icons-react';

interface AssigneeChipProps {
  assignee?: string;
}

export function AssigneeChip({ assignee }: AssigneeChipProps) {
  const sx = {
    height: 20,
    fontSize: '0.65rem',
    fontWeight: 600,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    bgcolor: (t: any) => alpha(t.palette.primary.main, 0.13),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    borderColor: (t: any) => alpha(t.palette.primary.main, 0.33),
    color: 'primary.main',
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
