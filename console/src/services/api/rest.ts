/**
 * REST API client that talks to the Go backend.
 *
 * All operations go through the real backend.
 */

import type {
  Project,
  RequirementsBundle,
  CollabSession,
  Design,
  DesignBundle,
  DesignComponent,
  ComponentDefinition,
  ComponentOpenAPI,
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
  ProjectBoard,
  TaskStatusResponse,
  TaskProgressResponse,
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
  //
  // The BFF is read-only over OC namespaces. Org creation is an out-of-band
  // onboarding flow (Thunder signup → platform-api-service in hosted;
  // seed-admin-org.sh in local). See
  // asdlc-service/controllers/organization_controller.go.

  async listOrganizations(): Promise<Organization[]> {
    try {
      const data = await fetchJSON<{ items: Organization[] }>(`/api/v1/organizations`);
      return data.items || [];
    } catch {
      return [];
    }
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

  // -- Requirements (multi-file directory under specs/requirements/) -------

  async getRequirements(orgHandle: string, projectId: string): Promise<RequirementsBundle | undefined> {
    try {
      const data = await fetchJSON<RequirementsBundle | null>(
        `${projectPrefix(orgHandle, projectId)}/requirements`,
      );
      return data ?? undefined;
    } catch {
      return undefined;
    }
  },

  async updateRequirementFile(
    orgHandle: string,
    projectId: string,
    filename: string,
    content: string,
  ): Promise<RequirementsBundle | undefined> {
    try {
      return await fetchJSON<RequirementsBundle>(
        `${projectPrefix(orgHandle, projectId)}/requirements/files/${encodeURIComponent(filename)}`,
        { method: 'PUT', body: JSON.stringify({ content }) },
      );
    } catch {
      return undefined;
    }
  },

  async deleteRequirementFile(
    orgHandle: string,
    projectId: string,
    filename: string,
  ): Promise<RequirementsBundle | undefined> {
    try {
      return await fetchJSON<RequirementsBundle>(
        `${projectPrefix(orgHandle, projectId)}/requirements/files/${encodeURIComponent(filename)}`,
        { method: 'DELETE' },
      );
    } catch {
      return undefined;
    }
  },

  async saveRequirements(orgHandle: string, projectId: string): Promise<RequirementsBundle | undefined> {
    try {
      return await fetchJSON<RequirementsBundle>(
        `${projectPrefix(orgHandle, projectId)}/requirements/save`,
        { method: 'POST' },
      );
    } catch {
      return undefined;
    }
  },

  async discardRequirements(orgHandle: string, projectId: string): Promise<RequirementsBundle | undefined> {
    try {
      return await fetchJSON<RequirementsBundle>(
        `${projectPrefix(orgHandle, projectId)}/requirements/discard`,
        { method: 'POST' },
      );
    } catch {
      return undefined;
    }
  },

  async listRequirementsVersions(orgHandle: string, projectId: string): Promise<ArtifactVersion[]> {
    try {
      return await fetchJSON<ArtifactVersion[]>(
        `${projectPrefix(orgHandle, projectId)}/requirements/versions`,
      );
    } catch {
      return [];
    }
  },

  async getRequirementsAtVersion(
    orgHandle: string,
    projectId: string,
    tag: string,
  ): Promise<RequirementsBundle | undefined> {
    try {
      return await fetchJSON<RequirementsBundle>(
        `${projectPrefix(orgHandle, projectId)}/requirements/versions/${encodeURIComponent(tag)}`,
      );
    } catch {
      return undefined;
    }
  },

  /**
   * Stream document generation for a specific requirement file via the
   * skill-routed endpoint. The skill ID, source filenames, and optional
   * user prompt are looked up by the caller from the document-types registry.
   *
   * `onDelta` receives each text-delta as it arrives. Skills with a
   * post-processor (wireframes / domain-model) emit a final delta with
   * `opts.replace = true` carrying the persisted payload — callers should
   * reset their accumulator and use the delta verbatim.
   */
  async generateRequirementFile(
    orgHandle: string,
    projectId: string,
    filename: string,
    body: { skillId: string; sources?: string[]; prompt?: string },
    onDelta: (delta: string, opts?: { replace?: boolean }) => void,
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
      `${BASE}${projectPrefix(orgHandle, projectId)}/requirements/files/${encodeURIComponent(filename)}/generate`,
      { method: 'POST', headers, body: JSON.stringify(body), signal },
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
            if (chunk.type === 'text-delta' && typeof chunk.delta === 'string') {
              onDelta(chunk.delta, chunk.replace ? { replace: true } : undefined);
            } else if (chunk.type === 'error') {
              errored = true;
            }
          } catch {
            /* ignore non-JSON */
          }
        }
      }
    }

    return !errored;
  },

  /**
   * Stream a single chat turn against the requirements directory. The BFF
   * returns SSE frames covering free-form text, structured tool events,
   * and a final turn-finish — see docs/design/requirements-chat.md §4.2.
   *
   * Resolves to true if the stream ran to completion (data-finish or
   * [DONE]). false on transport error / fatal `error` frame; the caller
   * sees individual `error` frames via `onError`.
   */
  async streamRequirementsChat(
    orgHandle: string,
    projectId: string,
    body: {
      message: string;
      history?: { role: 'user' | 'assistant'; content: string }[];
      files?: string[]; // in-scope filenames; empty means all
      mode?: 'edit' | 'ask';
      requestSessionBaseline?: boolean;
    },
    handlers: {
      onTurnStarted?: (turnId: string, startedMs: number) => void;
      onSessionBaseline?: (snapshotId: string) => void;
      onText?: (delta: string) => void;
      onToolStarted?: (e: {
        id: string;
        name: string;
        filename: string;
        summary?: string;
      }) => void;
      onToolResult?: (e: {
        id: string;
        filename: string;
        content?: string;
        siblings?: Record<string, string>;
        diff?: { added: number; removed: number; preview: string };
      }) => void;
      onToolError?: (e: {
        id?: string;
        name?: string;
        filename?: string;
        errorCode?: string;
        message?: string;
      }) => void;
      onValidationFailed?: (issues: { filename?: string; message: string }[]) => void;
      onFinish?: (e: { summary?: string; touched?: string[] }) => void;
      onError?: (e: { errorCode?: string; errorText?: string }) => void;
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
      `${BASE}${projectPrefix(orgHandle, projectId)}/requirements/chat`,
      { method: 'POST', headers, body: JSON.stringify(body), signal },
    );
    if (!res.ok || !res.body) {
      handlers.onError?.({ errorText: `chat request failed: ${res.status}` });
      return false;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let sawFinish = false;
    let sawFatal = false;

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

          let chunk: { type?: string; delta?: string; data?: Record<string, unknown>; errorCode?: string; errorText?: string };
          try {
            chunk = JSON.parse(payload);
          } catch {
            continue;
          }

          switch (chunk.type) {
            case 'text-delta':
              if (typeof chunk.delta === 'string') handlers.onText?.(chunk.delta);
              break;
            case 'data-turn-started': {
              const d = chunk.data as { turnId?: string; started?: number } | undefined;
              if (d?.turnId) handlers.onTurnStarted?.(d.turnId, d.started ?? Date.now());
              break;
            }
            case 'data-session-baseline': {
              const d = chunk.data as { snapshotId?: string } | undefined;
              if (d?.snapshotId) handlers.onSessionBaseline?.(d.snapshotId);
              break;
            }
            case 'data-tool-started': {
              const d = chunk.data as { id?: string; name?: string; filename?: string; summary?: string } | undefined;
              if (d?.id && d.name && d.filename !== undefined) {
                handlers.onToolStarted?.({
                  id: d.id,
                  name: d.name,
                  filename: d.filename,
                  summary: d.summary,
                });
              }
              break;
            }
            case 'data-tool-result': {
              const d = chunk.data as {
                id?: string;
                filename?: string;
                content?: string;
                siblings?: Record<string, string>;
                diff?: { added: number; removed: number; preview: string };
              } | undefined;
              if (d?.id && d.filename) {
                handlers.onToolResult?.({
                  id: d.id,
                  filename: d.filename,
                  content: d.content,
                  siblings: d.siblings,
                  diff: d.diff,
                });
              }
              break;
            }
            case 'data-tool-error': {
              const d = chunk.data as {
                id?: string;
                name?: string;
                filename?: string;
                errorCode?: string;
                message?: string;
              } | undefined;
              if (d) handlers.onToolError?.(d);
              break;
            }
            case 'data-validation-failed': {
              const d = chunk.data as { issues?: { filename?: string; message: string }[] } | undefined;
              if (d?.issues) handlers.onValidationFailed?.(d.issues);
              break;
            }
            case 'data-finish': {
              sawFinish = true;
              const d = chunk.data as { summary?: string; touched?: string[] } | undefined;
              handlers.onFinish?.(d ?? {});
              break;
            }
            case 'error':
              sawFatal = true;
              handlers.onError?.({
                errorCode: chunk.errorCode,
                errorText: chunk.errorText,
              });
              break;
            default:
              // Forward-compatible: ignore unknown frame types silently.
              break;
          }
        }
      }
    }

    return sawFinish && !sawFatal;
  },

  /**
   * Undo a single chat turn. Restores the working tree to the snapshot the
   * BFF captured at turn start.
   */
  async undoChatTurn(
    orgHandle: string,
    projectId: string,
    turnId: string,
  ): Promise<{ files: Record<string, string> } | undefined> {
    try {
      return await fetchJSON<{ files: Record<string, string> }>(
        `${projectPrefix(orgHandle, projectId)}/requirements/chat/turns/${encodeURIComponent(turnId)}/undo`,
        { method: 'POST' },
      );
    } catch {
      return undefined;
    }
  },

  /**
   * Read a single file's content from the chat session's baseline
   * snapshot. Drives the "View original" dialog. `existed=false` means
   * the file did not exist at baseline (the agent created it) — Revert
   * deletes the working-tree file.
   */
  async getRequirementsBaselineFile(
    orgHandle: string,
    projectId: string,
    baselineId: string,
    filename: string,
  ): Promise<{ snapshotId: string; filename: string; existed: boolean; content: string } | undefined> {
    try {
      return await fetchJSON(
        `${projectPrefix(orgHandle, projectId)}/requirements/chat/baseline/${encodeURIComponent(baselineId)}/files/${encodeURIComponent(filename)}`,
      );
    } catch {
      return undefined;
    }
  },

  /**
   * Revert a single requirement file back to its content at the session
   * baseline. Held under the requirements dir lock so it serialises with
   * concurrent chat / manual writes.
   */
  async revertRequirementsBaselineFile(
    orgHandle: string,
    projectId: string,
    baselineId: string,
    filename: string,
  ): Promise<boolean> {
    try {
      await fetchJSON<void>(
        `${projectPrefix(orgHandle, projectId)}/requirements/chat/baseline/${encodeURIComponent(baselineId)}/files/${encodeURIComponent(filename)}/revert`,
        { method: 'POST' },
      );
      return true;
    } catch {
      return false;
    }
  },

  /**
   * Drop the session-baseline snapshot. The console calls this once the
   * modified-files set becomes empty after Accepts.
   */
  async dropRequirementsBaseline(
    orgHandle: string,
    projectId: string,
    baselineId: string,
  ): Promise<boolean> {
    try {
      await fetchJSON<void>(
        `${projectPrefix(orgHandle, projectId)}/requirements/chat/baseline/${encodeURIComponent(baselineId)}`,
        { method: 'DELETE' },
      );
      return true;
    } catch {
      return false;
    }
  },

  // -- Collaboration (still scoped to the requirements editor session) ------
  async getCollabSession(orgHandle: string, projectId: string): Promise<CollabSession | undefined> {
    try {
      return await fetchJSON<CollabSession>(
        `${projectPrefix(orgHandle, projectId)}/requirements/collab-session`,
      );
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

  async saveAndProceedDesign(orgHandle: string, projectId: string): Promise<Design> {
    // Let ApiError bubble — Publish needs to surface the server's error
    // message (e.g. missing requirements baseline, save-via-API failures)
    // rather than collapsing every failure into a generic toast.
    return fetchJSON<Design>(`${projectPrefix(orgHandle, projectId)}/design/save`, {
      method: 'POST',
    });
  },

  async listDesignVersions(orgHandle: string, projectId: string): Promise<ArtifactVersion[]> {
    try {
      return await fetchJSON<ArtifactVersion[]>(`${projectPrefix(orgHandle, projectId)}/design/versions`);
    } catch {
      return [];
    }
  },

  async getDesignAtVersion(orgHandle: string, projectId: string, tag: string): Promise<Design | undefined> {
    try {
      return await fetchJSON<Design>(
        `${projectPrefix(orgHandle, projectId)}/design/versions/${encodeURIComponent(tag)}`,
      );
    } catch {
      return undefined;
    }
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

  // -- Design (multi-file bundle view) ---------------------------------------

  async getDesignBundle(
    orgHandle: string,
    projectId: string,
  ): Promise<DesignBundle | undefined> {
    try {
      return await fetchJSON<DesignBundle>(
        `${projectPrefix(orgHandle, projectId)}/design/bundle`,
      );
    } catch {
      return undefined;
    }
  },

  async getDesignBundleAtVersion(
    orgHandle: string,
    projectId: string,
    tag: string,
  ): Promise<DesignBundle | undefined> {
    try {
      return await fetchJSON<DesignBundle>(
        `${projectPrefix(orgHandle, projectId)}/design/versions/${encodeURIComponent(tag)}/bundle`,
      );
    } catch {
      return undefined;
    }
  },

  async updateDesignFile(
    orgHandle: string,
    projectId: string,
    path: string,
    content: string,
  ): Promise<DesignBundle | undefined> {
    try {
      return await fetchJSON<DesignBundle>(
        `${projectPrefix(orgHandle, projectId)}/design/files/${path}`,
        {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ content }),
        },
      );
    } catch {
      return undefined;
    }
  },

  async deleteDesignFile(
    orgHandle: string,
    projectId: string,
    path: string,
  ): Promise<DesignBundle | undefined> {
    try {
      return await fetchJSON<DesignBundle>(
        `${projectPrefix(orgHandle, projectId)}/design/files/${path}`,
        { method: 'DELETE' },
      );
    } catch {
      return undefined;
    }
  },

  async deleteDesignComponent(
    orgHandle: string,
    projectId: string,
    componentName: string,
  ): Promise<DesignBundle | undefined> {
    try {
      return await fetchJSON<DesignBundle>(
        `${projectPrefix(orgHandle, projectId)}/design/components/${encodeURIComponent(componentName)}`,
        { method: 'DELETE' },
      );
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
  // No POST: deploys are driven entirely by OC's Component controller
  // (AutoDeploy=true) once the build's generate-workload-cr step posts the
  // Workload CR. The deploy page only reads.

  async listDeployments(orgHandle: string, projectId: string, componentId: string): Promise<Deployment[]> {
    try {
      const data = await fetchJSON<{ items: Deployment[] }>(`${projectPrefix(orgHandle, projectId)}/components/${componentId}/deployments`);
      return data.items || [];
    } catch {
      return [];
    }
  },

  // -- OpenAPI (Test tab) -----------------------------------------------------
  // 200  → ComponentOpenAPI (spec is a YAML string)
  // 409  → { error: 'not-service', componentType }  — exists but isn't a service
  // 404  → { error: 'not-found' }                   — design.json missing or no match

  async getComponentOpenAPI(
    orgHandle: string,
    projectId: string,
    componentId: string,
  ): Promise<
    | ComponentOpenAPI
    | { error: 'not-service'; componentType: string }
    | { error: 'not-found' }
  > {
    try {
      return await fetchJSON<ComponentOpenAPI>(
        `${projectPrefix(orgHandle, projectId)}/components/${componentId}/openapi`,
      );
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // The 409 body is the same envelope, just without a spec.
        try {
          const parsed = JSON.parse(e.message) as Partial<ComponentOpenAPI>;
          return { error: 'not-service', componentType: parsed.componentType || 'unknown' };
        } catch {
          return { error: 'not-service', componentType: 'unknown' };
        }
      }
      return { error: 'not-found' };
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

  // F3c — operator-driven retry for a task in `verification_failed`.
  // Re-dispatches a fresh WorkflowRun against the same component / issue /
  // branch with a newly minted per-task bearer.
  async retryTask(orgHandle: string, projectId: string, taskId: string): Promise<void> {
    await fetchJSON<void>(`${projectPrefix(orgHandle, projectId)}/tasks/${taskId}/retry`, {
      method: 'POST',
    });
  },

  async getTask(orgHandle: string, projectId: string, taskId: string): Promise<ComponentTask> {
    return fetchJSON<ComponentTask>(`${projectPrefix(orgHandle, projectId)}/tasks/${taskId}`);
  },

  async getTaskStatus(orgHandle: string, projectId: string, taskId: string): Promise<TaskStatusResponse> {
    return fetchJSON<TaskStatusResponse>(`${projectPrefix(orgHandle, projectId)}/tasks/${taskId}/status`);
  },

  async getTaskAgentProgress(
    orgHandle: string, projectId: string, taskId: string,
    sinceMillis: number, limit?: number,
  ): Promise<TaskProgressResponse> {
    const q = new URLSearchParams({ sinceMillis: String(sinceMillis) });
    if (limit) q.set('limit', String(limit));
    return fetchJSON<TaskProgressResponse>(
      `${projectPrefix(orgHandle, projectId)}/tasks/${taskId}/progress/agent?${q.toString()}`,
    );
  },

  async getTaskBuildProgress(
    orgHandle: string, projectId: string, taskId: string,
    sinceMillis: number,
  ): Promise<TaskProgressResponse> {
    const q = new URLSearchParams({ sinceMillis: String(sinceMillis) });
    return fetchJSON<TaskProgressResponse>(
      `${projectPrefix(orgHandle, projectId)}/tasks/${taskId}/progress/build?${q.toString()}`,
    );
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
