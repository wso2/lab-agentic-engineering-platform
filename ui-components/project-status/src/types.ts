export type StageState = 'done' | 'active' | 'pending' | 'blocked';

export interface StageMetric {
  key: string;
  value: string;
}

export interface Stage {
  id: string;
  name: string;
  iteration: number;
  state: StageState;
  headline: string;
  summary?: string;
  metrics?: StageMetric[];
  artifacts?: string[];
  changes?: string[];
  timestamp?: string;
  duration?: string;
}

export interface ProjectStatusPolylineProps {
  stages: Stage[];
  onStageClick?: (stage: Stage) => void;
  /**
   * Whether stages are clickable. When false, the internal drawer is suppressed,
   * `onStageClick` is ignored, and nodes render as static (no cursor / focus / aria-button).
   * Default: true.
   */
  interactive?: boolean;
  mode?: 'light' | 'dark';
  maxIteration?: number;
}

export interface StageDrawerProps {
  stage: Stage | null;
  open: boolean;
  onClose: () => void;
  mode?: 'light' | 'dark';
}
