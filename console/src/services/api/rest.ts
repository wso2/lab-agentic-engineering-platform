/**
 * REST API client that talks to the Go backend.
 *
 * All operations go through the real backend.
 */

import type {
  Project,
  Spec,
  CollabSession,
  Design,
  DesignComponent,
  ComponentDefinition,
  CreateProjectInput,
  Build,
  BuildLogs,
  Deployment,
  ComponentTask,
  ComponentConfig,
  EnvVar,
  ProjectStatus,
  ArtifactVersion,
  Tasks,
  Organization,
  CreateOrganizationInput,
  ProjectBoard,
} from './types';

import { env } from '../../config/env';

const BASE = env.VITE_CORE_API_BASE_URL;

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// Token accessor — set by App.tsx after auth, called on every request
let _getAccessToken: (() => Promise<string>) | null = null;

export function setTokenAccessor(fn: (() => Promise<string>) | null): void {
  _getAccessToken = fn;
}

export async function getToken(): Promise<string> {
  if (!_getAccessToken) return '';
  return _getAccessToken();
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string>),
  };

  if (_getAccessToken) {
    const token = await _getAccessToken();
    if (token) {
      headers.Authorization = `Bearer ${token}`;
    }
  }

  const res = await fetch(`${BASE}${path}`, { cache: 'no-store', ...init, headers });
  if (!res.ok) {
    const body = await res.text();
    let message = body;
    try {
      const parsed = JSON.parse(body);
      if (parsed.message) message = parsed.message;
    } catch { /* use raw body */ }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

/**
 * Map backend Project model → frontend Project type.
 */
function mapProject(raw: any): Project {
  return {
    id: raw.name,
    name: raw.displayName || raw.name,
    prompt: raw.description || '',
    phase: 'spec',
    createdAt: raw.createdAt || new Date().toISOString(),
    updatedAt: raw.createdAt || new Date().toISOString(),
  };
}

function mapComponent(raw: any): ComponentDefinition {
  return {
    id: raw.name,
    projectId: raw.projectName || '',
    name: raw.displayName || raw.name,
    techStack: raw.type || '',
    responsibilities: raw.description || '',
    apiBoundaries: '',
    interactions: '',
    status: 'created',
    createdAt: raw.createdAt || new Date().toISOString(),
    updatedAt: raw.createdAt || new Date().toISOString(),
  };
}

function orgPrefix(orgHandle: string): string {
  return `/api/v1/organizations/${orgHandle}`;
}

function projectPrefix(orgHandle: string, projectName: string): string {
  return `${orgPrefix(orgHandle)}/projects/${projectName}`;
}

function slugify(input: string): string {
  return input
    .toLowerCase()
    .replace(/[\s_]+/g, '-')      // spaces and underscores → hyphens
    .replace(/[^a-z0-9-]/g, '')   // strip non-RFC-1123 chars
    .replace(/-+/g, '-')          // collapse consecutive hyphens
    .replace(/^-|-$/g, '');       // trim leading/trailing hyphens
}

export const restApi = {
  // -- Organizations (real backend) ------------------------------------------

  async listOrganizations(): Promise<Organization[]> {
    try {
      const data = await fetchJSON<{ items: Organization[] }>(`/api/v1/organizations`);
      return data.items || [];
    } catch {
      return [];
    }
  },

  async createOrganization(input: CreateOrganizationInput): Promise<Organization> {
    return fetchJSON<Organization>(`/api/v1/organizations`, {
      method: 'POST',
      body: JSON.stringify({
        name: slugify(input.displayName),
        displayName: input.displayName,
        description: input.description || '',
      }),
    });
  },

  // -- Projects (real backend) -----------------------------------------------

  async listProjects(orgHandle: string): Promise<Project[]> {
    try {
      const data = await fetchJSON<{ items: any[] }>(`${orgPrefix(orgHandle)}/projects`);
      return (data.items || []).map(mapProject);
    } catch {
      return [];
    }
  },

  async getProject(orgHandle: string, projectId: string): Promise<Project | undefined> {
    try {
      const raw = await fetchJSON<any>(`${orgPrefix(orgHandle)}/projects/${projectId}`);
      return mapProject(raw);
    } catch {
      return undefined;
    }
  },

  async createProject(orgHandle: string, input: CreateProjectInput): Promise<Project> {
    const raw = await fetchJSON<any>(`${orgPrefix(orgHandle)}/projects`, {
      method: 'POST',
      body: JSON.stringify({
        name: slugify(input.name),
        displayName: input.name,
        description: input.prompt || '',
        deploymentPipeline: 'default',
      }),
    });
    return mapProject(raw);
  },

  async deleteProject(orgHandle: string, projectId: string): Promise<void> {
    await fetchJSON<void>(`${orgPrefix(orgHandle)}/projects/${projectId}`, { method: 'DELETE' });
  },

  async getProjectStatus(orgHandle: string, projectId: string): Promise<ProjectStatus | undefined> {
    try {
      return await fetchJSON<ProjectStatus>(`${projectPrefix(orgHandle, projectId)}/status`);
    } catch {
      return undefined;
    }
  },

  // -- Components (real backend) ---------------------------------------------

  async listComponents(orgHandle: string, projectId: string): Promise<ComponentDefinition[]> {
    try {
      const data = await fetchJSON<{ items: any[] }>(`${projectPrefix(orgHandle, projectId)}/components`);
      return (data.items || []).map(mapComponent);
    } catch {
      return [];
    }
  },

  async getComponent(orgHandle: string, projectId: string, componentId: string): Promise<ComponentDefinition | undefined> {
    try {
      const raw = await fetchJSON<any>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}`);
      return mapComponent(raw);
    } catch {
      return undefined;
    }
  },

  // -- Specs (real backend) --------------------------------------------------

  /**
   * Stream spec generation from the BFF. The response is an AI SDK v6
   * UI Message Stream (SSE); we forward text-delta chunks to `onDelta` as
   * they arrive. Resolves to true when the stream completes successfully.
   */
  async generateSpec(
    orgHandle: string,
    projectId: string,
    prompt: string,
    onDelta: (delta: string) => void,
    signal?: AbortSignal,
  ): Promise<boolean> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
    };
    if (_getAccessToken) {
      const token = await _getAccessToken();
      if (token) headers.Authorization = `Bearer ${token}`;
    }

    const res = await fetch(
      `${BASE}${projectPrefix(orgHandle, projectId)}/spec/generate`,
      {
        method: 'POST',
        headers,
        body: JSON.stringify({ prompt }),
        signal,
      },
    );
    if (!res.ok || !res.body) return false;

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let errored = false;

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      // SSE frames are separated by a blank line; keep any trailing partial in buffer.
      let idx: number;
      while ((idx = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);

        for (const line of frame.split('\n')) {
          if (!line.startsWith('data: ')) continue;
          const payload = line.slice(6);
          if (payload === '[DONE]') continue;
          try {
            const chunk = JSON.parse(payload);
            if (chunk.type === 'text-delta' && typeof chunk.delta === 'string') {
              onDelta(chunk.delta);
            } else if (chunk.type === 'error') {
              errored = true;
            }
          } catch {
            // ignore non-JSON data line
          }
        }
      }
    }

    return !errored;
  },

  async getSpec(orgHandle: string, projectId: string): Promise<Spec | undefined> {
    try {
      const data = await fetchJSON<Spec | null>(`${projectPrefix(orgHandle, projectId)}/spec`);
      return data ?? undefined;
    } catch {
      return undefined;
    }
  },

  async updateSpec(orgHandle: string, projectId: string, content: string): Promise<Spec | undefined> {
    try {
      return await fetchJSON<Spec>(`${projectPrefix(orgHandle, projectId)}/spec`, {
        method: 'PUT',
        body: JSON.stringify({ content }),
      });
    } catch {
      return undefined;
    }
  },

  async approveSpec(orgHandle: string, projectId: string): Promise<Spec | undefined> {
    try {
      return await fetchJSON<Spec>(`${projectPrefix(orgHandle, projectId)}/spec/save`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  async saveAndProceedSpec(orgHandle: string, projectId: string): Promise<Spec | undefined> {
    try {
      return await fetchJSON<Spec>(`${projectPrefix(orgHandle, projectId)}/spec/save`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  async listSpecVersions(orgHandle: string, projectId: string): Promise<ArtifactVersion[]> {
    try {
      return await fetchJSON<ArtifactVersion[]>(`${projectPrefix(orgHandle, projectId)}/spec/versions`);
    } catch {
      return [];
    }
  },

  async getSpecAtVersion(orgHandle: string, projectId: string, version: number): Promise<Spec | undefined> {
    try {
      return await fetchJSON<Spec>(`${projectPrefix(orgHandle, projectId)}/spec/versions/${version}`);
    } catch {
      return undefined;
    }
  },

  async discardSpecChanges(orgHandle: string, projectId: string): Promise<Spec | undefined> {
    try {
      return await fetchJSON<Spec>(`${projectPrefix(orgHandle, projectId)}/spec/discard`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  // -- Collaboration (real backend) ------------------------------------------------
  async getCollabSession(orgHandle: string, projectId: string): Promise<CollabSession | undefined> {
    try {
      return await fetchJSON<CollabSession>(`${projectPrefix(orgHandle, projectId)}/spec/collab-session`);
    } catch {
      return undefined;
    }
  },

  // -- Designs (real backend) ------------------------------------------------

  async getDesign(orgHandle: string, projectId: string): Promise<Design | undefined> {
    try {
      const data = await fetchJSON<Design | null>(`${projectPrefix(orgHandle, projectId)}/design`);
      return data ?? undefined;
    } catch {
      return undefined;
    }
  },

  /**
   * Stream architecture generation from the BFF. The response is a UI Message
   * Stream (SSE) emitting custom data-* frames as the design is built:
   *   data-overview                    — { text }
   *   data-requirements                — { items }
   *   data-component-added             — { component } (slim — no openAPISpec yet)
   *   data-component-updated           — { name, patch, openapiInvalidated }
   *                                       emitted when shape fields change on
   *                                       an existing component
   *   data-component-removed           — { name }
   *   data-component-spec-updating     — { name }
   *                                       fires when the architect calls
   *                                       set_openapi for a component (the UI
   *                                       shows a spinner until data-finish)
   *   data-finish                      — { design } with the full validated output
   *
   * Resolves to true when the stream completes successfully.
   */
  async generateDesignStream(
    orgHandle: string,
    projectId: string,
    handlers: {
      onOverview?: (overview: string) => void;
      onRequirements?: (requirements: string[]) => void;
      onComponentAdded?: (component: DesignComponent) => void;
      onComponentUpdated?: (
        name: string,
        patch: Partial<DesignComponent>,
        openapiInvalidated: boolean,
      ) => void;
      onComponentRemoved?: (name: string) => void;
      onComponentSpecUpdating?: (name: string) => void;
      onFinish?: (design: {
        overview: string;
        requirements: string[];
        components: DesignComponent[];
      }) => void;
    },
    signal?: AbortSignal,
  ): Promise<boolean> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
    };
    if (_getAccessToken) {
      const token = await _getAccessToken();
      if (token) headers.Authorization = `Bearer ${token}`;
    }

    const res = await fetch(
      `${BASE}${projectPrefix(orgHandle, projectId)}/design/generate`,
      {
        method: 'POST',
        headers,
        body: '{}',
        signal,
      },
    );
    if (!res.ok || !res.body) return false;

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let errored = false;

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let idx: number;
      while ((idx = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);

        for (const line of frame.split('\n')) {
          if (!line.startsWith('data: ')) continue;
          const payload = line.slice(6);
          if (payload === '[DONE]') continue;
          try {
            const chunk = JSON.parse(payload);
            switch (chunk.type) {
              case 'data-overview':
                // New shape: { text }. Tolerate the legacy { overview } key
                // for one rolling-deploy window.
                if (typeof chunk.data?.text === 'string')
                  handlers.onOverview?.(chunk.data.text);
                else if (typeof chunk.data?.overview === 'string')
                  handlers.onOverview?.(chunk.data.overview);
                break;
              case 'data-requirements':
                if (Array.isArray(chunk.data?.items))
                  handlers.onRequirements?.(chunk.data.items);
                else if (Array.isArray(chunk.data?.requirements))
                  handlers.onRequirements?.(chunk.data.requirements);
                break;
              case 'data-component-added':
                if (chunk.data?.component)
                  handlers.onComponentAdded?.(
                    chunk.data.component as DesignComponent,
                  );
                break;
              case 'data-component-updated':
                if (typeof chunk.data?.name === 'string')
                  handlers.onComponentUpdated?.(
                    chunk.data.name,
                    (chunk.data.patch ?? {}) as Partial<DesignComponent>,
                    Boolean(chunk.data.openapiInvalidated),
                  );
                break;
              case 'data-component-removed':
                if (typeof chunk.data?.name === 'string')
                  handlers.onComponentRemoved?.(chunk.data.name);
                break;
              case 'data-component-spec-updating':
                if (typeof chunk.data?.name === 'string')
                  handlers.onComponentSpecUpdating?.(chunk.data.name);
                break;
              case 'data-finish':
                if (chunk.data?.design) handlers.onFinish?.(chunk.data.design);
                break;
              case 'error':
                errored = true;
                break;
            }
          } catch {
            // ignore non-JSON data line
          }
        }
      }
    }

    return !errored;
  },

  async generateDesign(orgHandle: string, projectId: string): Promise<Design | undefined> {
    try {
      return await fetchJSON<Design>(`${projectPrefix(orgHandle, projectId)}/design/generate`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  async approveDesign(orgHandle: string, projectId: string): Promise<Design | undefined> {
    try {
      return await fetchJSON<Design>(`${projectPrefix(orgHandle, projectId)}/design/save`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  async saveAndProceedDesign(orgHandle: string, projectId: string): Promise<Design | undefined> {
    try {
      return await fetchJSON<Design>(`${projectPrefix(orgHandle, projectId)}/design/save`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  async listDesignVersions(orgHandle: string, projectId: string): Promise<ArtifactVersion[]> {
    try {
      return await fetchJSON<ArtifactVersion[]>(`${projectPrefix(orgHandle, projectId)}/design/versions`);
    } catch {
      return [];
    }
  },

  async getDesignAtVersion(orgHandle: string, projectId: string, version: number): Promise<Design | undefined> {
    try {
      return await fetchJSON<Design>(`${projectPrefix(orgHandle, projectId)}/design/versions/${version}`);
    } catch {
      return undefined;
    }
  },

  async getSpecWireframe(
    orgHandle: string,
    projectId: string,
  ): Promise<
    | { status: 'ready'; content: string }
    | { status: 'generating' }
    | { status: 'not_generated' }
    | { status: 'error'; error: string }
  > {
    const headers: Record<string, string> = {};
    if (_getAccessToken) {
      const token = await _getAccessToken();
      if (token) headers.Authorization = `Bearer ${token}`;
    }
    try {
      const res = await fetch(
        `${BASE}${projectPrefix(orgHandle, projectId)}/spec/wireframe`,
        { headers },
      );
      if (res.status === 200) return { status: 'ready', content: await res.text() };
      if (res.status === 202) return { status: 'generating' };
      if (res.status === 404) return { status: 'not_generated' };
      let errorMsg = 'Wireframe generation failed';
      try {
        const body = await res.json();
        if (body.message) errorMsg = body.message;
      } catch { /* use default */ }
      return { status: 'error', error: errorMsg };
    } catch {
      return { status: 'error', error: 'Failed to fetch wireframe' };
    }
  },

  async generateSpecWireframe(orgHandle: string, projectId: string): Promise<void> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (_getAccessToken) {
      const token = await _getAccessToken();
      if (token) headers.Authorization = `Bearer ${token}`;
    }
    const res = await fetch(
      `${BASE}${projectPrefix(orgHandle, projectId)}/spec/wireframe/generate`,
      { method: 'POST', headers, body: '{}' },
    );
    if (!res.ok) throw new Error(`Failed to start wireframe generation (${res.status})`);
    // 202 received — generation running in BFF background goroutine
  },

  async discardDesignChanges(orgHandle: string, projectId: string): Promise<Design | undefined> {
    try {
      return await fetchJSON<Design>(`${projectPrefix(orgHandle, projectId)}/design/discard`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  // -- Builds (WorkflowRuns) --------------------------------------------------

  async triggerBuild(orgHandle: string, projectId: string, componentId: string): Promise<Build | undefined> {
    try {
      return await fetchJSON<Build>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}/builds`, {
        method: 'POST',
      });
    } catch {
      return undefined;
    }
  },

  async listBuilds(orgHandle: string, projectId: string, componentId: string): Promise<Build[]> {
    try {
      const data = await fetchJSON<{ items: Build[] }>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}/builds`);
      return data.items || [];
    } catch {
      return [];
    }
  },

  async getBuildStatus(orgHandle: string, projectId: string, componentId: string, buildName: string): Promise<Build | undefined> {
    try {
      return await fetchJSON<Build>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}/builds/${buildName}`);
    } catch {
      return undefined;
    }
  },

  async getBuildLogs(orgHandle: string, projectId: string, componentId: string, buildName: string): Promise<BuildLogs | undefined> {
    try {
      return await fetchJSON<BuildLogs>(
        `${projectPrefix(orgHandle, projectId)}/components/${componentId}/builds/${buildName}/logs`
      );
    } catch {
      return undefined;
    }
  },

  // -- Deployments (ReleaseBindings) ------------------------------------------

  async deploy(orgHandle: string, projectId: string, componentId: string, environment: string): Promise<Deployment | undefined> {
    try {
      return await fetchJSON<Deployment>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}/deployments`, {
        method: 'POST',
        body: JSON.stringify({ environment }),
      });
    } catch {
      return undefined;
    }
  },

  async listDeployments(orgHandle: string, projectId: string, componentId: string): Promise<Deployment[]> {
    try {
      const data = await fetchJSON<{ items: Deployment[] }>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}/deployments`);
      return data.items || [];
    } catch {
      return [];
    }
  },

  // -- Tasks (implementation agents) -------------------------------------------

  async createTasks(orgHandle: string, projectId: string): Promise<ComponentTask[]> {
    try {
      return await fetchJSON<ComponentTask[]>(`${projectPrefix(orgHandle, projectId)}/tasks/create`, {
        method: 'POST',
      });
    } catch {
      return [];
    }
  },

  async dispatchTasks(orgHandle: string, projectId: string): Promise<any[]> {
    return await fetchJSON<any[]>(`${projectPrefix(orgHandle, projectId)}/tasks/dispatch`, {
      method: 'POST',
    });
  },

  /**
   * Streams task generation as SSE. The two-phase tech-lead agent emits:
   *   data-plan-item            — { tempId, componentName, title, rationale, dependsOn }
   *   data-plan-complete        — { items[] }
   *   data-task-issued          — { tempId, taskId, issueUrl, issueNumber }
   *   data-task-issue-failed    — { tempId, errorText }
   *   data-task-body-delta      — { taskId, delta }
   *   data-task-body-complete   — { taskId, body }
   *   data-task-rejected        — { taskId, reason }
   *   data-finish               — { batchId, taskCount }
   *   error                     — { scope: 'plan'|'detail', errorText, taskId?, tempId?, issues? }
   *
   * Resolves to true when the stream completed successfully (no error frames).
   */
  async streamGenerateTasks(
    orgHandle: string,
    projectId: string,
    handlers: {
      onPlanItem?: (item: {
        tempId: string;
        componentName: string;
        title: string;
        rationale: string;
        dependsOn: string[];
      }) => void;
      onPlanComplete?: (items: unknown[]) => void;
      onTaskIssued?: (e: {
        tempId: string;
        taskId: string;
        issueUrl: string;
        issueNumber: number;
      }) => void;
      onTaskIssueFailed?: (e: { tempId: string; errorText: string }) => void;
      onTaskBodyDelta?: (e: { taskId: string; delta: string }) => void;
      onTaskBodyComplete?: (e: { taskId: string; body: string }) => void;
      onTaskRejected?: (e: { taskId: string; reason: string }) => void;
      onError?: (e: {
        scope?: string;
        errorText?: string;
        tempId?: string;
        taskId?: string;
        issues?: unknown;
      }) => void;
      onFinish?: (e: { batchId?: string; taskCount?: number }) => void;
    },
    signal?: AbortSignal,
  ): Promise<boolean> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
    };
    if (_getAccessToken) {
      const token = await _getAccessToken();
      if (token) headers.Authorization = `Bearer ${token}`;
    }

    const res = await fetch(
      `${BASE}${projectPrefix(orgHandle, projectId)}/tasks/generate`,
      { method: 'POST', headers, body: '{}', signal },
    );
    if (!res.ok || !res.body) return false;

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let errored = false;

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let idx: number;
      while ((idx = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);
        for (const line of frame.split('\n')) {
          if (!line.startsWith('data: ')) continue;
          const payload = line.slice(6);
          if (payload === '[DONE]') continue;
          try {
            const chunk = JSON.parse(payload);
            const data = chunk.data ?? {};
            switch (chunk.type) {
              case 'data-plan-item':
                handlers.onPlanItem?.(data);
                break;
              case 'data-plan-complete':
                handlers.onPlanComplete?.(data.items ?? []);
                break;
              case 'data-task-issued':
                handlers.onTaskIssued?.(data);
                break;
              case 'data-task-issue-failed':
                handlers.onTaskIssueFailed?.(data);
                break;
              case 'data-task-body-delta':
                handlers.onTaskBodyDelta?.(data);
                break;
              case 'data-task-body-complete':
                handlers.onTaskBodyComplete?.(data);
                break;
              case 'data-task-rejected':
                handlers.onTaskRejected?.(data);
                break;
              case 'data-finish':
                handlers.onFinish?.(data);
                break;
              case 'error':
                errored = true;
                handlers.onError?.(data);
                break;
            }
          } catch {
            // ignore non-JSON data line
          }
        }
      }
    }
    return !errored;
  },

  async regenerateTaskBody(
    orgHandle: string,
    projectId: string,
    taskId: string,
    handlers: {
      onTaskBodyDelta?: (e: { taskId: string; delta: string }) => void;
      onTaskBodyComplete?: (e: { taskId: string; body: string }) => void;
      onError?: (e: { errorText?: string }) => void;
      onFinish?: () => void;
    },
    signal?: AbortSignal,
  ): Promise<boolean> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
    };
    if (_getAccessToken) {
      const token = await _getAccessToken();
      if (token) headers.Authorization = `Bearer ${token}`;
    }
    const res = await fetch(
      `${BASE}${projectPrefix(orgHandle, projectId)}/tasks/${taskId}/regenerate-body`,
      { method: 'POST', headers, body: '{}', signal },
    );
    if (!res.ok || !res.body) return false;
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let errored = false;
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let idx: number;
      while ((idx = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);
        for (const line of frame.split('\n')) {
          if (!line.startsWith('data: ')) continue;
          const payload = line.slice(6);
          if (payload === '[DONE]') continue;
          try {
            const chunk = JSON.parse(payload);
            const data = chunk.data ?? {};
            switch (chunk.type) {
              case 'data-task-body-delta':
                handlers.onTaskBodyDelta?.(data);
                break;
              case 'data-task-body-complete':
                handlers.onTaskBodyComplete?.(data);
                break;
              case 'data-finish':
                handlers.onFinish?.();
                break;
              case 'error':
                errored = true;
                handlers.onError?.(data);
                break;
            }
          } catch {
            // ignore
          }
        }
      }
    }
    return !errored;
  },

  async getTasks(orgHandle: string, projectId: string): Promise<Tasks | undefined> {
    try {
      const data = await fetchJSON<Tasks | null>(`${projectPrefix(orgHandle, projectId)}/tasks/generated`);
      return data ?? undefined;
    } catch {
      return undefined;
    }
  },

  async getProjectBoard(orgHandle: string, projectId: string): Promise<ProjectBoard> {
    const empty: ProjectBoard = { todo: [], inProgress: [], done: [], onHold: [], failed: [], url: '' };
    try {
      const data = await fetchJSON<ProjectBoard>(`${projectPrefix(orgHandle, projectId)}/board`);
      return data ?? empty;
    } catch {
      return empty;
    }
  },

  async listTasks(orgHandle: string, projectId: string): Promise<ComponentTask[]> {
    try {
      return await fetchJSON<ComponentTask[]>(`${projectPrefix(orgHandle, projectId)}/tasks`);
    } catch {
      return [];
    }
  },

  async execTask(orgHandle: string, projectId: string, taskId: string): Promise<void> {
    await fetchJSON<void>(`${projectPrefix(orgHandle, projectId)}/tasks/${taskId}/exec`, {
      method: 'POST',
    });
  },

  // -- Component Configs (Environment Variables) --------------------------------

  async getComponentConfig(
    orgHandle: string, projectId: string, componentId: string,
  ): Promise<ComponentConfig | undefined> {
    try {
      const data = await fetchJSON<ComponentConfig | null>(
        `${projectPrefix(orgHandle, projectId)}/components/${componentId}/configs`,
      );
      return data ?? undefined;
    } catch {
      return undefined;
    }
  },

  async updateComponentConfig(
    orgHandle: string, projectId: string, componentId: string, envVars: EnvVar[],
  ): Promise<ComponentConfig | undefined> {
    try {
      return await fetchJSON<ComponentConfig>(
        `${projectPrefix(orgHandle, projectId)}/components/${componentId}/configs`,
        {
          method: 'PUT',
          body: JSON.stringify({ envVars }),
        },
      );
    } catch {
      return undefined;
    }
  },

  // -- Utility ---------------------------------------------------------------

  async resetAll(): Promise<void> {
    try {
      await fetchJSON<void>('/api/v1/_test/reset', { method: 'POST' });
    } catch {
      // ignore
    }
  },
};
