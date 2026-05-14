export interface SectionConfig {
  key: 'inProgress' | 'todo' | 'done' | 'onHold' | 'failed';
  label: string;
  isPrimary: boolean;
  dotColor: string | null;
  borderColor: string | null;
}
