/**
 * ASDLC API client — all operations go through the real Go backend.
 */

export { restApi as api, ApiError } from './rest';
export type {
  Project,
  Spec,
  Design,
  DesignComponent,
  ComponentDefinition,
  ComponentTask,
  ComponentConfig,
  EnvVar,
  TaskStatus,
  Tasks,
  CreateProjectInput,
  ProjectPhase,
  ProjectSdlcPhase,
  ProjectStatus,
  SpecStatus,
  DesignStatus,
  ComponentStatus,
  ArtifactVersion,
  Organization,
  CreateOrganizationInput,
  Task,
  ProjectBoard,
  LabelInfo,
} from './types';
