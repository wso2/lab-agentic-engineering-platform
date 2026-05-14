import { lazy, memo, Suspense, useMemo } from 'react';
import { Box, CircularProgress, Typography } from '@wso2/oxygen-ui';
import { buildProjectModel, type CellDiagramComponent } from './buildProjectModel.js';

const CellDiagram = lazy(() =>
  import('@wso2/cell-diagram').then((m) => ({ default: m.CellDiagram })),
);

export interface CellDiagramViewProps {
  components: CellDiagramComponent[];
  /** Optional override for the empty-state copy. */
  emptyState?: React.ReactNode;
}

export const CellDiagramView = memo(function CellDiagramView({
  components,
  emptyState,
}: CellDiagramViewProps) {
  const project = useMemo(() => buildProjectModel(components), [components]);

  if (components.length === 0) {
    return (
      <Box
        sx={{
          flex: 1,
          minHeight: 0,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          p: 3,
          textAlign: 'center',
          color: 'text.secondary',
        }}
      >
        {emptyState ?? (
          <Typography variant="body2">
            Generate a design to see the cell diagram.
          </Typography>
        )}
      </Box>
    );
  }

  return (
    <Box sx={{ flex: 1, minHeight: 0, display: 'flex' }}>
      <Suspense
        fallback={
          <Box sx={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <CircularProgress />
          </Box>
        }
      >
        <CellDiagram project={project} />
      </Suspense>
    </Box>
  );
});
