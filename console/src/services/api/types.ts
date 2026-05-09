// ---------------------------------------------------------------------------
// Domain types for the ASDLC platform
// ---------------------------------------------------------------------------

export type ProjectPhase = 'spec' | 'design' | 'components' | 'implementing' | 'done';
export type SpecStatus = 'draft' | 'approved';
export type DesignStatus = 'none' | 'generating' | 'draft' | 'approved';
export type ComponentStatus = 'created' | 'implementing' | 'done';

export interface ArtifactVersion {
  version: number;
  tagName: string;
  commitHash: string;
  sourceSpec?: string;
  sourceDesign?: string;
}

export interface Project {
  id: string;
  name: string;
  prompt?: string;
  phase: ProjectPhase;
  createdAt: string;
  updatedAt: string;
}

export interface RequirementsBundle {
  projectId: string;
  files: Record<string, string>;
  status: SpecStatus;
  version?: number;
  versions?: ArtifactVersion[];
  hasUnsavedChanges?: boolean;
}

export interface CollabSession {
  roomId: string;
  wsUrl: string;
  userName?: string;
  email?: string;
}

// -- AI-generated design (structured output from Agent SDK) ------------------

export interface DesignComponent {
  name: string;
  componentType: 'service' | 'web-app' | 'scheduled-task';
  language: string;
  dependsOn: string[];
  entrypoint: 'deployment/service';
  buildpack: 'docker';
  appPath: string;
  // Optional: during streaming, the component card appears (with shape
  // metadata) before its OpenAPI YAML is set. Final design.json from the
  // BFF always has a non-empty string here.
  openAPISpec?: string;
  componentAgentInstructions: string;
}

export interface Design {
  projectId: string;
  overview: string;
  requirements: string[];
  components: DesignComponent[];
  status: DesignStatus;
  version: number;
  versions?: ArtifactVersion[];
  hasUnsavedChanges?: boolean;
  sourceSpec?: string;
}

// -- Legacy ComponentDefinition (from OC, used in component list/detail) -----

export interface ComponentDefinition {
  id: string;
  projectId: string;
  name: string;
  techStack: string;
  responsibilities: string;
  apiBoundaries: string;
  interactions: string;
  status: ComponentStatus;
  createdAt: string;
  updatedAt: string;
}

export interface CreateProjectInput {
  name: string;
  prompt?: string;
}

// -- Organizations -----------------------------------------------------------

export interface Organization {
  uuid: string;
  name: string;
  displayName?: string;
  description?: string;
  status?: string;
  createdAt: string;
}

export interface CreateOrganizationInput {
  displayName: string;
  description?: string;
}

// -- Build (WorkflowRun) ----------------------------------------------------

export type BuildStatus = 'Pending' | 'Running' | 'Succeeded' | 'Failed' | 'Completed'
  | 'WorkflowPending' | 'WorkflowSucceeded' | 'WorkflowFailed';

export interface Build {
  name: string;
  status: BuildStatus;
  startedAt: string;
  componentName: string;
  projectName: string;
  image: string;
  commit: string;
}

// -- Build Logs ---------------------------------------------------------------

export interface BuildLogEntry {
  timestamp: string;
  log: string;
  level: string;
}

export interface BuildLogs {
  logs: BuildLogEntry[];
  totalCount: number;
}

// -- Implementation Tasks (dispatched to agents) ------------------------------

// Phase 0 single-status lifecycle. Webhooks (and the build watcher polling
// OC) drive transitions; see asdlc-service/services/task_state.go for the
// transition table.
export type TaskStatus =
  | 'pending'
  | 'pending_deps'
  | 'in_progress'
  | 'ready_for_review'
  | 'merged'
  | 'building'
  | 'deployed'
  | 'rejected'
  | 'failed'
  | 'abandoned';

export interface ComponentTask {
  id: string;
  projectId: string;
  componentName: string;
  order: number;
  status: TaskStatus;
  workspacePath: string;

  // Tech-lead agent revamp — task-level data lives on the row; component
  // shape (OpenAPI, language, appPath, etc.) is read fresh from
  // .asdlc/design.json on every dispatch.
  title?: string;
  rationale?: string;
  body?: string;
  taskDependsOn?: string[];

  // Lineage — set at generation time, immutable thereafter.
  batchId?: string;
  sourceDesignVersion?: string;
  sourceSpecVersion?: string;

  // GitHub artifacts (1:1:1:1 with this task) — set at dispatch.
  issueUrl?: string;
  issueNumber?: number;
  branchName?: string;
  pullRequestNumber?: number;
  pullRequestUrl?: string;

  // State derived from webhooks.
  mergeCommitSha?: string;
  lastEventAt?: string;
  lastBuildRunName?: string;
  lastBuildSha?: string;
  lastCodingAgentRunName?: string;

  // GitHub issue labels (for Kanban board)
  labels?: string[];

  // Set when a GH issue body edit failed after retries; reconciler will retry.
  bodySyncPending?: boolean;

  // Error tracking
  errorMessage?: string;

  dispatchedAt?: string;
  createdAt: string;
  updatedAt: string;
}

// -- Task progress (live execution feed) -------------------------------------
// Mirrors asdlc-service/clients/observer/schema.go and
// remote-worker/src/lib/progress/schema.ts. Versioned NDJSON envelope —
// see docs/design/task-execution-progress.md §5.1.

export const TASK_PROGRESS_SCHEMA_VERSION = 1;

export type TaskProgressKind =
  | 'phase'
  | 'tool_use'
  | 'git_commit'
  | 'git_push'
  | 'gh_action'
  | 'log'
  | 'result'
  | 'build_step';

export interface TaskProgressEvent {
  schemaVersion: number;
  ts: string;
  seq: number;
  kind: TaskProgressKind;
  // Discriminated payload — only the fields relevant to `kind` are set.
  phase?: string;
  tool?: string;
  sha?: string;
  branch?: string;
  files?: number;
  command?: string;
  level?: 'info' | 'warn' | 'error';
  status?: 'success' | 'failure';
  summary?: string;
  error?: string;
  step?: string;
  startedAt?: string;
  completedAt?: string;
  message?: string;
}

export interface TaskProgressResponse {
  schemaVersion: number;
  lines: TaskProgressEvent[];
  cursorMillis: number;
  phase?: string;
  truncated?: boolean;
  final: boolean;
}

export interface BuildStep {
  name: string;
  phase?: string;
  message?: string;
  startedAt?: string;
  completedAt?: string;
}

export interface TaskStatusResponse {
  task: ComponentTask;
  buildSteps?: BuildStep[];
}

// -- Generated Tasks ----------------------------------------------------------
// Returned by GET /tasks/generated — enriches live GitHub issues with DB state.

export interface Tasks {
  projectId: string;
  tasks: ComponentTask[];
  status: 'approved';
}

// -- Project Board (sourced from GitHub Project Board) -----------------------

export interface LabelInfo {
  name: string;
  color: string; // hex without #, e.g. "0075ca"
}

export type TaskLifecycleStatus = 'gh_issue_waiting' | 'gh_issue_syncing' | 'gh_issue_created' | 'gh_issue_failed';

export interface Task {
  id: string;
  title: string;
  url: string;
  description?: string;
  assignee?: string;
  componentTaskId?: string;
  labels?: LabelInfo[];
  lifecycleStatus?: TaskLifecycleStatus;
}

export interface ProjectBoard {
  url: string;
  todo: Task[];
  inProgress: Task[];
  done: Task[];
  onHold: Task[];
  failed: Task[];
}

// -- Component Config (Environment Variables) ---------------------------------

export interface EnvVar {
  key: string;
  value: string;
}

export interface ComponentConfig {
  id: string;
  projectName: string;
  componentName: string;
  envVars: EnvVar[];
  createdAt: string;
  updatedAt: string;
}

// -- Project Status (computed SDLC phase) -------------------------------------

export type ProjectSdlcPhase = 'no-repo' | 'repo-cloning' | 'prompt' | 'spec' | 'architecture' | 'tasks' | 'components';

export interface ProjectStatus {
  phase: ProjectSdlcPhase;
  repoStatus: string;
  repoUrl: string;
  hasSpec: boolean;
  hasDesign: boolean;
  hasTasks: boolean;
  specStatus: string;
  designStatus: string;
}

// -- Deployment (ReleaseBinding) ---------------------------------------------

export interface Deployment {
  name: string;
  environment: string;
  releaseName: string;
  componentName: string;
  endpointUrl: string;
  createdAt: string;
  status: string;
}
