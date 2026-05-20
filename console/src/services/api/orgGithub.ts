/**
 * Typed client for /api/v1/organizations/{orgHandle}/github*.
 *
 * The console's Org Settings → GitHub Integration page reads from
 * GET .../github (projection — kind, identity, status, etc.) and writes
 * via POST .../github/connect/start (App-mode, OAuth-driven) or
 * POST .../github/pat. The PAT is never echoed back; the projection
 * endpoint deliberately omits it.
 */

import { env } from '../../config/env';
import { ApiError } from './rest';

const BASE = env.VITE_CORE_API_BASE_URL;

let _getAccessToken: (() => Promise<string>) | null = null;

// Pull the token accessor from rest.ts at runtime — they share the same
// auth context. (Importing setTokenAccessor here would create a cycle;
// re-using the rest module's accessor via a small bridge is the simplest
// fix.)
export function setOrgGithubTokenAccessor(fn: (() => Promise<string>) | null): void {
  _getAccessToken = fn;
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string>),
  };
  if (_getAccessToken) {
    const token = await _getAccessToken();
    if (token) headers.Authorization = `Bearer ${token}`;
  }
  const res = await fetch(`${BASE}${path}`, { ...init, headers });
  if (!res.ok) {
    const body = await res.text();
    let message = body;
    let code: string | undefined;
    try {
      const parsed = JSON.parse(body);
      if (parsed.message) message = parsed.message;
      if (parsed.error) message = parsed.error;
      if (parsed.code) code = parsed.code;
    } catch {
      /* use raw body */
    }
    const err = new ApiError(res.status, message);
    (err as ApiError & { code?: string }).code = code;
    throw err;
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

export type ConnectionKind = 'app-installation' | 'user-pat' | '';

export interface OrgGithubProjection {
  ocOrgId: string;
  kind: ConnectionKind | 'not_connected';
  githubLogin?: string;
  identityName?: string;
  identityEmail?: string;
  identityLogin?: string;
  installationId?: number;
  selectedRepos?: string[];
  status: 'active' | 'suspended' | 'disconnected' | 'disconnecting' | 'not_connected';
  connectedAt?: string;
  lastValidatedAt?: string;
  identityChangedAt?: string;
  prevIdentityLogin?: string;
}

/**
 * Candidate install surfaced in the picker. The connect callback returns
 * candidates (filtered to user-administered, not-bound-elsewhere installs)
 * via the `?candidates=<base64>` query param when 2+ are present.
 */
export interface AppInstallationSummary {
  installationId: number;
  accountLogin: string;
  accountType: string; // "Organization" | "User"
}

export interface ConnectStartResponse {
  authorizeUrl: string;
}

/**
 * Phase 2 PR D §10.9 — abandoned-task projection used by the
 * ReachReconciliationBanner's "View tasks" dialog. The org-scoped
 * tasks endpoint returns a flat array of ComponentTask rows; we
 * project the fields the dialog cares about to avoid pulling the
 * whole task model into the banner.
 */
export interface AbandonedTask {
  id: string;
  componentName: string;
  projectId: string;
  issueNumber?: number;
  issueUrl?: string;
  pullRequestNumber?: number;
  pullRequestUrl?: string;
  cause?: string;
  status: string;
  lastEventAt?: string;
}

// ----------------------------------------------------------------------------
// API surface
// ----------------------------------------------------------------------------

export const orgGithubApi = {
  /**
   * Read the projection for the org's GitHub connection.
   */
  async getStatus(orgHandle: string): Promise<OrgGithubProjection> {
    const data = await fetchJSON<OrgGithubProjection>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/github`,
    );
    return data;
  },

  /**
   * Start the App-mode connect flow. Returns the GitHub OAuth authorize
   * URL the caller should perform a full-page redirect to. The
   * connect-state JWT (15-min TTL) carries (ocOrgId, installationId,
   * actor) through the round-trip; installationId is 0 for the initial
   * connect and non-zero when re-entering from the picker for a chosen
   * candidate.
   */
  async startConnect(orgHandle: string, installationId?: number): Promise<ConnectStartResponse> {
    const data = await fetchJSON<ConnectStartResponse | { data: ConnectStartResponse }>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/github/connect/start`,
      {
        method: 'POST',
        body: JSON.stringify(installationId ? { installationId } : {}),
      },
    );
    const inner = (data as { data?: ConnectStartResponse }).data ?? (data as ConnectStartResponse);
    return inner;
  },

  /**
   * Connect or replace via PAT. Surfaces field-level errors from the
   * git-service validation chain (caller maps `code` to UI placement).
   */
  async connectPAT(orgHandle: string, pat: string, githubLogin: string): Promise<OrgGithubProjection> {
    return fetchJSON<OrgGithubProjection>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/github/pat`,
      { method: 'POST', body: JSON.stringify({ pat, githubLogin }) },
    );
  },

  /**
   * Disconnect — runs the cascade Phases A–D synchronously, plus Phase E
   * (GitHub-side App uninstall) when uninstall is true. uninstall
   * defaults true for App-mode connections; PAT mode ignores the flag.
   */
  async disconnect(orgHandle: string, uninstall: boolean = true): Promise<void> {
    const qs = uninstall ? '' : '?uninstall=false';
    await fetchJSON<void>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/github${qs}`,
      { method: 'DELETE' },
    );
  },

  /**
   * Phase 2 PR D §10.9 — list tasks under the org filtered by status,
   * cause, and (optionally) a relative since-window. Used by the
   * ReachReconciliationBanner to count and surface tasks abandoned
   * by a `repo.unselected` cascade in the last 24 hours.
   *
   * The BFF accepts ?since=24h shorthand or RFC3339 timestamps.
   */
  async listOrgTasks(
    orgHandle: string,
    filter: { status?: string; cause?: string; since?: string },
  ): Promise<AbandonedTask[]> {
    const params = new URLSearchParams();
    if (filter.status) params.set('status', filter.status);
    if (filter.cause) params.set('cause', filter.cause);
    if (filter.since) params.set('since', filter.since);
    const qs = params.toString();
    const data = await fetchJSON<{ data: AbandonedTask[] } | AbandonedTask[]>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/tasks${qs ? `?${qs}` : ''}`,
    );
    // The BFF wraps list responses in {data: ...} via WriteSuccessResponse.
    if (Array.isArray(data)) return data;
    return (data as { data: AbandonedTask[] }).data ?? [];
  },
};
