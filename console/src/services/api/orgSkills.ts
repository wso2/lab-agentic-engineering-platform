/**
 * Typed client for /api/v1/organizations/{orgHandle}/skills*.
 *
 * The console's Org Settings → Skills page lists the catalogue (built-in
 * read-only + org-authored custom/imported), views any skill body, and
 * creates / edits / deletes custom skills + imports AgentSkills tarballs.
 *
 * Built-ins are read-only — PUT/DELETE return 403 SKILL_NOT_EDITABLE.
 * See docs/design/skills-system.md > "REST API".
 */

import { env } from '../../config/env';

const BASE = env.VITE_CORE_API_BASE_URL;

let _getAccessToken: (() => Promise<string>) | null = null;

export function setOrgSkillsTokenAccessor(fn: (() => Promise<string>) | null): void {
  _getAccessToken = fn;
}

/** A single structured validation issue, mirrors the BFF's shape. */
export interface SkillValidationIssue {
  code: string;
  message: string;
  path?: string;
}

/**
 * SkillApiError carries the HTTP status plus the BFF's stable error code
 * (`NAME_COLLISION`, `SKILL_NOT_EDITABLE`, `IMPORTED_SKILL_IN_USE`) and the
 * structured validation issues array when present.
 */
export class SkillApiError extends Error {
  status: number;
  code?: string;
  issues?: SkillValidationIssue[];
  constructor(status: number, message: string, code?: string, issues?: SkillValidationIssue[]) {
    super(message);
    this.status = status;
    this.code = code;
    this.issues = issues;
  }
}

async function authHeader(extra?: Record<string, string>): Promise<Record<string, string>> {
  const headers: Record<string, string> = { ...(extra ?? {}) };
  if (_getAccessToken) {
    const token = await _getAccessToken();
    if (token) headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

async function parseError(res: Response): Promise<SkillApiError> {
  const body = await res.text();
  let message = body || res.statusText;
  let code: string | undefined;
  let issues: SkillValidationIssue[] | undefined;
  try {
    const parsed = JSON.parse(body);
    if (parsed.message) message = parsed.message;
    else if (parsed.error) message = parsed.error;
    if (parsed.code) code = parsed.code;
    if (Array.isArray(parsed.issues)) {
      issues = parsed.issues as SkillValidationIssue[];
      if (issues.length > 0) message = issues.map((i) => i.message).join('; ');
    }
  } catch {
    /* use raw body */
  }
  return new SkillApiError(res.status, message, code, issues);
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = await authHeader({
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string>),
  });
  const res = await fetch(`${BASE}${path}`, { cache: 'no-store', ...init, headers });
  if (!res.ok) throw await parseError(res);
  if (res.status === 204) return undefined as T;
  return res.json();
}

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

export type SkillKind = 'builtin' | 'custom' | 'imported';

export interface SkillSummary {
  name: string;
  kind: SkillKind;
  version: number;
  description: string;
  contentSha: string;
  editable: boolean;
}

export interface SkillDetail {
  orgId: string;
  name: string;
  kind: SkillKind;
  description: string;
  skillMd: string;
  references: Record<string, string>;
  version: number;
  contentSha: string;
  license?: string;
  compatibility?: string;
  updatedAt: string;
  editable: boolean;
}

export interface SkillImportResult {
  name: string;
  kind: SkillKind;
  license?: string;
  compatibility?: string;
  warnings: string[];
}

// ----------------------------------------------------------------------------
// API surface
// ----------------------------------------------------------------------------

function orgSkills(orgHandle: string): string {
  return `/api/v1/organizations/${encodeURIComponent(orgHandle)}/skills`;
}

export const orgSkillsApi = {
  /** List catalogue summaries visible to the org (built-ins + org-authored). */
  async list(orgHandle: string): Promise<SkillSummary[]> {
    const raw = await fetchJSON<{ skills?: SkillSummary[] } | SkillSummary[]>(orgSkills(orgHandle));
    if (Array.isArray(raw)) return raw;
    return raw.skills ?? [];
  },

  /** Fetch a single skill's full body + references. */
  async get(orgHandle: string, name: string): Promise<SkillDetail> {
    return fetchJSON<SkillDetail>(`${orgSkills(orgHandle)}/${encodeURIComponent(name)}`);
  },

  /** Create a custom skill. Throws SkillApiError (issues populated) on validation failure. */
  async create(
    orgHandle: string,
    body: { name: string; skillMd: string; references?: Record<string, string> },
  ): Promise<SkillDetail> {
    return fetchJSON<SkillDetail>(orgSkills(orgHandle), {
      method: 'POST',
      body: JSON.stringify(body),
    });
  },

  /** Update an existing custom skill. */
  async update(
    orgHandle: string,
    name: string,
    body: { skillMd: string; references?: Record<string, string> },
  ): Promise<SkillDetail> {
    return fetchJSON<SkillDetail>(`${orgSkills(orgHandle)}/${encodeURIComponent(name)}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    });
  },

  /** Delete a custom or imported skill. 403 for built-ins, 409 if imported-in-use. */
  async remove(orgHandle: string, name: string): Promise<void> {
    await fetchJSON<void>(`${orgSkills(orgHandle)}/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    });
  },

  /** Import an AgentSkills tarball (.tar.gz) as a kind=imported skill. */
  async importTarball(orgHandle: string, file: File): Promise<SkillImportResult> {
    const headers = await authHeader();
    const form = new FormData();
    form.append('file', file);
    const res = await fetch(`${BASE}${orgSkills(orgHandle)}/import`, {
      method: 'POST',
      headers,
      body: form,
    });
    if (!res.ok) throw await parseError(res);
    const result = (await res.json()) as SkillImportResult;
    // Go marshals a nil slice as null — normalise so the UI can read .length.
    return { ...result, warnings: result.warnings ?? [] };
  },
};
