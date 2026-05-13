import { createElement, type ReactNode } from 'react';
import type { CustomView } from '@asdlc/explorer';
import { CellDiagramView } from './CellDiagramView.js';
import type { CellDiagramComponent } from './buildProjectModel.js';

/** Stable id used as the Explorer's `activePath` sentinel for the cell diagram. */
export const CELL_DIAGRAM_VIEW_ID = 'cell-diagram';
export const CELL_DIAGRAM_VIEW_LABEL = 'Cell Diagram';

export interface CreateCellDiagramViewOptions {
  components: CellDiagramComponent[];
  label?: string;
  icon?: ReactNode;
  emptyState?: ReactNode;
}

/**
 * Build a {@link CustomView} for the Explorer that renders the cell diagram.
 * Pass the returned object inside `customViews` on `<Explorer>`.
 */
export function createCellDiagramView({
  components,
  label = CELL_DIAGRAM_VIEW_LABEL,
  icon,
  emptyState,
}: CreateCellDiagramViewOptions): CustomView {
  return {
    id: CELL_DIAGRAM_VIEW_ID,
    label,
    icon,
    content: createElement(CellDiagramView, { components, emptyState }),
  };
}
