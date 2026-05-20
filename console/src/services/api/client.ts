/**
 * ASDLC API client — all operations go through the real Go backend.
 */

export { restApi as api, ApiError } from './rest';
export type {
  Project,
  RequirementsBundle,
  Design,
  DesignBundle,
  DesignComponent,
  ComponentDefinition,
  ComponentOpenAPI,
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
  Task,
  ProjectBoard,
  LabelInfo,
  TaskProgressEvent,
  TaskProgressResponse,
  TaskProgressKind,
  TaskStatusResponse,
  BuildStep,
} from './types';
