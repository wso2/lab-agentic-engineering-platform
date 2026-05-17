// Pure line-diff helper used to attach a tiny preview to each tool result
// frame. The preview is for chat-card display only — the authoritative
// content lives in the on-disk file after the BFF applies the tool.

export interface DiffStats {
  added: number;
  removed: number;
}

export interface DiffSummary extends DiffStats {
  preview: string;
}

const MAX_PREVIEW_LINES = 12;

export function lineDiff(before: string, after: string): DiffSummary {
  const a = before.split("\n");
  const b = after.split("\n");
  // Trim trailing empty lines so we don't count a stylistic newline at EOF
  // as a real change.
  while (a.length > 0 && a[a.length - 1] === "") a.pop();
  while (b.length > 0 && b[b.length - 1] === "") b.pop();

  // LCS-backed shortest-edit-script. For the sizes we deal with
  // (requirements files are typically <2000 lines) the O(n*m) cost is
  // dwarfed by the model latency, so we don't bother with Myers.
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () =>
    Array(b.length + 1).fill(0),
  );
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      if (a[i] === b[j]) {
        lcs[i]![j]! = (lcs[i + 1]![j + 1] as number) + 1;
      } else {
        lcs[i]![j]! = Math.max(
          lcs[i + 1]![j] as number,
          lcs[i]![j + 1] as number,
        );
      }
    }
  }

  let added = 0;
  let removed = 0;
  const lines: string[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      i++;
      j++;
      continue;
    }
    if ((lcs[i + 1]![j] as number) >= (lcs[i]![j + 1] as number)) {
      if (lines.length < MAX_PREVIEW_LINES) lines.push(`- ${a[i]}`);
      removed++;
      i++;
    } else {
      if (lines.length < MAX_PREVIEW_LINES) lines.push(`+ ${b[j]}`);
      added++;
      j++;
    }
  }
  while (i < a.length) {
    if (lines.length < MAX_PREVIEW_LINES) lines.push(`- ${a[i]}`);
    removed++;
    i++;
  }
  while (j < b.length) {
    if (lines.length < MAX_PREVIEW_LINES) lines.push(`+ ${b[j]}`);
    added++;
    j++;
  }

  if (added + removed > MAX_PREVIEW_LINES) {
    lines.push(`… (${added + removed - MAX_PREVIEW_LINES} more)`);
  }
  return { added, removed, preview: lines.join("\n") };
}

// `created` / `deleted` diff shortcuts: avoid running the full LCS when
// one side is empty.
export function createdDiff(content: string): DiffSummary {
  const lines = content.split("\n");
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  const preview = lines
    .slice(0, MAX_PREVIEW_LINES)
    .map((l) => `+ ${l}`)
    .join("\n");
  const suffix =
    lines.length > MAX_PREVIEW_LINES
      ? `\n… (${lines.length - MAX_PREVIEW_LINES} more)`
      : "";
  return { added: lines.length, removed: 0, preview: preview + suffix };
}

export function deletedDiff(content: string): DiffSummary {
  const lines = content.split("\n");
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  const preview = lines
    .slice(0, MAX_PREVIEW_LINES)
    .map((l) => `- ${l}`)
    .join("\n");
  const suffix =
    lines.length > MAX_PREVIEW_LINES
      ? `\n… (${lines.length - MAX_PREVIEW_LINES} more)`
      : "";
  return { added: 0, removed: lines.length, preview: preview + suffix };
}
