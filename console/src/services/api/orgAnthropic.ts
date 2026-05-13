/**
 * Typed client for /api/v1/organizations/{orgHandle}/anthropic*.
 *
 * The console's Org Settings → Anthropic Integration page reads from
 * GET .../anthropic (projection — prefix + last4, status, validation) and
 * writes via POST .../anthropic (single key body) or DELETE .../anthropic.
 *
 * The raw key is never echoed back; the projection deliberately omits it.
 *
 * See docs/design/anthropic-key-dual-token.md.
 */

import { env } from '../../config/env';
import { ApiError } from './rest';

const BASE = env.VITE_CORE_API_BASE_URL;

let _getAccessToken: (() => Promise<string>) | null = null;

export function setOrgAnthropicTokenAccessor(fn: (() => Promise<string>) | null): void {
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

export type AnthropicStatus = 'active' | 'invalid' | 'disconnected' | 'not_connected';

export interface OrgAnthropicProjection {
  ocOrgId: string;
  status: AnthropicStatus;
  keyPrefix?: string;
  keyLast4?: string;
  connectedAt?: string;
  lastValidatedAt?: string;
  validationError?: string;
}

// ----------------------------------------------------------------------------
// API surface
// ----------------------------------------------------------------------------

export const orgAnthropicApi = {
  /**
   * Read the projection for the org's Anthropic connection. Returns
   * {status:"not_connected"} (no key fields) when no row exists.
   */
  async getStatus(orgHandle: string): Promise<OrgAnthropicProjection> {
    const raw = await fetchJSON<OrgAnthropicProjection | { data: OrgAnthropicProjection }>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/anthropic`,
    );
    const inner = (raw as { data?: OrgAnthropicProjection }).data ?? (raw as OrgAnthropicProjection);
    return inner;
  },

  /**
   * Connect or replace the org's Anthropic API key. The validation chain
   * runs server-side; field-level error codes (`anthropic_key_invalid`,
   * `anthropic_unreachable`) are surfaced on ApiError.code.
   */
  async connect(orgHandle: string, apiKey: string): Promise<OrgAnthropicProjection> {
    const raw = await fetchJSON<OrgAnthropicProjection | { data: OrgAnthropicProjection }>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/anthropic`,
      { method: 'POST', body: JSON.stringify({ apiKey }) },
    );
    const inner = (raw as { data?: OrgAnthropicProjection }).data ?? (raw as OrgAnthropicProjection);
    return inner;
  },

  /**
   * Disconnect — removes the encrypted key bytes from org_secrets, the
   * metadata row, and the per-org WP Secret. In-flight WorkflowRuns are
   * NOT cancelled.
   */
  async disconnect(orgHandle: string): Promise<void> {
    await fetchJSON<void>(
      `/api/v1/organizations/${encodeURIComponent(orgHandle)}/anthropic`,
      { method: 'DELETE' },
    );
  },
};
