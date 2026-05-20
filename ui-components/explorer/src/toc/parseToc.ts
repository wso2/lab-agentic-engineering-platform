export interface TocEntry {
  /** Heading depth, 1..6 (# = 1, ###### = 6). */
  level: number;
  /** Rendered heading text (no leading #, no trailing #'s). */
  text: string;
  /** 0-based ordinal among all headings in the document. */
  index: number;
}

const HEADING_RE = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const CODE_FENCE_RE = /^\s*(```|~~~)/;

/**
 * Parse the ATX-style headings of a markdown string into a flat list. Headings
 * inside fenced code blocks are ignored. Setext (underline-style) headings are
 * not recognized; we only support `#` prefix syntax, which is what MdEditor
 * emits.
 */
export function parseToc(markdown: string): TocEntry[] {
  if (!markdown) return [];
  const lines = markdown.split('\n');
  const entries: TocEntry[] = [];
  let inCode = false;
  let index = 0;

  for (const line of lines) {
    if (CODE_FENCE_RE.test(line)) {
      inCode = !inCode;
      continue;
    }
    if (inCode) continue;

    const m = HEADING_RE.exec(line);
    if (!m) continue;

    const level = m[1]!.length;
    const text = m[2]!.trim();
    if (!text) continue;

    entries.push({ level, text, index });
    index++;
  }

  return entries;
}
