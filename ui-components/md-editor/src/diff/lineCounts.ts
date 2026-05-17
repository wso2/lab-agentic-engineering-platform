import { diffLines } from 'diff';

export interface LineDiffCounts {
  added: number;
  removed: number;
}

/**
 * Line-level diff summary suitable for compact UI badges (e.g. a sidebar
 * "+12 / -3" chip). Identical inputs return `{added: 0, removed: 0}`. Uses
 * the same `diff` package the diff viewer already depends on, so there's
 * no extra runtime cost for callers that ship the viewer.
 */
export function countLineChanges(oldText: string, newText: string): LineDiffCounts {
  if (oldText === newText) return { added: 0, removed: 0 };
  const changes = diffLines(oldText, newText, { newlineIsToken: false });
  let added = 0;
  let removed = 0;
  for (const change of changes) {
    const count = change.count ?? change.value.split('\n').filter(Boolean).length;
    if (change.added) added += count;
    else if (change.removed) removed += count;
  }
  return { added, removed };
}
