import { Chip, Stack, Typography } from '@wso2/oxygen-ui';

interface LineageLabelProps {
  sourceSpec?: string;
  sourceDesign?: string;
}

export default function LineageLabel({ sourceSpec, sourceDesign }: LineageLabelProps) {
  if (!sourceSpec && !sourceDesign) return null;

  const parts: string[] = [];
  if (sourceSpec) parts.push(sourceSpec);
  if (sourceDesign) parts.push(sourceDesign);

  return (
    <Chip
      label={
        <Stack direction="row" alignItems="center" gap={0.5}>
          <Typography variant="caption" sx={{ fontSize: '0.7rem', opacity: 0.7 }}>
            from
          </Typography>
          <Typography variant="caption" fontWeight={600} sx={{ fontSize: '0.7rem' }}>
            {parts.join(', ')}
          </Typography>
        </Stack>
      }
      size="small"
      variant="outlined"
      sx={{ height: 24, borderStyle: 'dashed' }}
    />
  );
}
