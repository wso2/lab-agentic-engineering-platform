export interface SectionConfig {
  key: 'inProgress' | 'todo' | 'pendingDeps' | 'done' | 'onHold' | 'failed';
  label: string;
  isPrimary: boolean;
  dotColor: string | null;
  borderColor: string | null;
}
