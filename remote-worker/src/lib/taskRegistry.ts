import type { TaskInfo } from "./types.js";

const tasks = new Map<string, TaskInfo>();

export function get(taskId: string): TaskInfo | undefined {
  return tasks.get(taskId);
}

export function set(taskId: string, info: TaskInfo): void {
  tasks.set(taskId, info);
}

export function markDone(taskId: string, exitCode: number, error?: string): void {
  const info = tasks.get(taskId);
  if (!info) return;
  info.done = true;
  info.exitCode = exitCode;
  if (error) info.error = error;
  info.query = undefined;
}

export function runningCount(): number {
  let n = 0;
  for (const info of tasks.values()) if (!info.done) n++;
  return n;
}

export function allTasks(): TaskInfo[] {
  return [...tasks.values()];
}
