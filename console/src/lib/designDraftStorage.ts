// Per-file local-storage draft persistence for the Architecture page.
// Mirrors `requirementsDraftStorage.ts`. Keys are paths relative to
// `.asdlc/design/` (forward slashes), so we namespace differently.

export interface StoredDesignFileDraft {
  draft: string;
  baseServerContent: string;
  savedAt: number;
}

export type StoredDesignDraftMap = Record<string, StoredDesignFileDraft>;

const PREFIX = 'asdlc:design-drafts';

function key(orgId: string, projectId: string): string {
  return `${PREFIX}:${orgId}:${projectId}`;
}

function storage(): Storage | null {
  try {
    return typeof window !== 'undefined' ? window.localStorage : null;
  } catch {
    return null;
  }
}

export function loadDesignDrafts(orgId: string, projectId: string): StoredDesignDraftMap {
  const s = storage();
  if (!s) return {};
  const raw = s.getItem(key(orgId, projectId));
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw) as StoredDesignDraftMap;
    if (typeof parsed !== 'object' || parsed === null) {
      s.removeItem(key(orgId, projectId));
      return {};
    }
    return parsed;
  } catch {
    s.removeItem(key(orgId, projectId));
    return {};
  }
}

export function saveDesignDraft(
  orgId: string,
  projectId: string,
  path: string,
  draft: StoredDesignFileDraft,
): void {
  const s = storage();
  if (!s) return;
  const all = loadDesignDrafts(orgId, projectId);
  all[path] = draft;
  try {
    s.setItem(key(orgId, projectId), JSON.stringify(all));
  } catch (err) {
    console.warn('[designDraftStorage] save failed:', err);
  }
}

export function clearDesignDraft(orgId: string, projectId: string, path: string): void {
  const s = storage();
  if (!s) return;
  const all = loadDesignDrafts(orgId, projectId);
  if (!(path in all)) return;
  delete all[path];
  if (Object.keys(all).length === 0) {
    s.removeItem(key(orgId, projectId));
    return;
  }
  try {
    s.setItem(key(orgId, projectId), JSON.stringify(all));
  } catch (err) {
    console.warn('[designDraftStorage] save failed:', err);
  }
}

export function clearAllDesignDrafts(orgId: string, projectId: string): void {
  const s = storage();
  if (!s) return;
  s.removeItem(key(orgId, projectId));
}
