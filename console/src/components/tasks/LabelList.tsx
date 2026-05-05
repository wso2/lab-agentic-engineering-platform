import { Box, Chip } from '@wso2/oxygen-ui';
import type { LabelInfo } from '../../services/api';
import { labelTextColor } from '../../lib/taskBoard';

interface LabelListProps {
  labels: LabelInfo[];
}

export function LabelList({ labels }: LabelListProps) {
  if (labels.length === 0) return null;
  return (
    <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: '4px' }}>
      {labels.map(label => (
        <Chip
          key={label.name}
          label={label.name}
          size="small"
          variant="outlined"
          sx={{
            height: 20,
            fontSize: '0.65rem',
            fontWeight: 600,
            bgcolor: `#${label.color}33`,
            borderColor: `#${label.color}66`,
            color: labelTextColor(label.color),
            '& .MuiChip-label': { px: 0.75 },
          }}
        />
      ))}
    </Box>
  );
}
