import type { Stage } from '@asdlc/project-status';
import type { ComponentTask, ProjectStatus } from '../services/api';

const ACTIVE_TASK_STATUSES = new Set([
  'pending',
  'pending_deps',
  'in_progress',
  'ready_for_review',
  'merged',
]);

export function buildProjectStages(
  status: ProjectStatus | undefined,
  tasks: ComponentTask[],
): Stage[] {
  const specStatus = status?.specStatus ?? '';
  const designStatus = status?.designStatus ?? '';
  const hasSpec = status?.hasSpec ?? false;
  const hasDesign = status?.hasDesign ?? false;

  const requirements: Stage = {
    id: 'requirements',
    name: 'Requirements',
    iteration: hasSpec ? 1 : 0,
    state:
      specStatus === 'generating' ? 'active'
      : hasSpec || specStatus === 'approved' || specStatus === 'draft' ? 'done'
      : 'pending',
    headline:
      specStatus === 'generating' ? 'Generating spec…'
      : hasSpec ? 'Spec ready'
      : 'Not started',
  };

  const architecture: Stage = {
    id: 'architecture',
    name: 'Architecture',
    iteration: hasDesign ? 1 : 0,
    state:
      designStatus === 'generating' ? 'active'
      : hasDesign || designStatus === 'approved' || designStatus === 'draft' ? 'done'
      : 'pending',
    headline:
      designStatus === 'generating' ? 'Generating design…'
      : hasDesign ? 'Design ready'
      : 'Awaiting requirements',
  };

  const tasksTotal = tasks.length;
  const tasksDone = tasks.filter((t) => t.status === 'merged' || t.status === 'deployed').length;
  const tasksFailed = tasks.some((t) => t.status === 'failed' || t.status === 'rejected');
  const tasksActive = tasks.some((t) => ACTIVE_TASK_STATUSES.has(t.status));
  const tasksAllDeployed = tasksTotal > 0 && tasks.every((t) => t.status === 'deployed');

  const tasksStage: Stage = {
    id: 'tasks',
    name: 'Tasks & Code',
    iteration: tasksDone,
    state:
      tasksActive ? 'active'
      : tasksAllDeployed ? 'done'
      : tasksFailed ? 'blocked'
      : tasksTotal === 0 ? 'pending'
      : 'pending',
    headline:
      tasksTotal === 0 ? 'Awaiting design'
      : `${tasksDone} / ${tasksTotal} tasks complete`,
  };

  const deployedCount = tasks.filter((t) => t.status === 'deployed').length;
  const buildingCount = tasks.filter((t) => t.status === 'building').length;
  const buildFailed = tasks.some((t) => t.status === 'failed');

  const deployment: Stage = {
    id: 'deployment',
    name: 'Deployment',
    iteration: deployedCount,
    state:
      buildingCount > 0 ? 'active'
      : tasksAllDeployed ? 'done'
      : buildFailed ? 'blocked'
      : 'pending',
    headline:
      buildingCount > 0 ? `${buildingCount} building…`
      : tasksAllDeployed ? 'Deployed'
      : deployedCount > 0 ? `${deployedCount} / ${tasksTotal} deployed`
      : 'Awaiting tasks',
  };

  return [requirements, architecture, tasksStage, deployment];
}
