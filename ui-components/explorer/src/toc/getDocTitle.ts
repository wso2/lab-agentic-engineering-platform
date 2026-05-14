import { parseToc } from './parseToc.js';

/**
 * Derive a human-friendly document title. Uses the first H1 in the content
 * if one exists; otherwise the filename with any trailing `.md` / `.markdown`
 * extension stripped.
 */
export function getDocTitle(path: string, markdown: string | undefined): string {
  if (markdown) {
    const toc = parseToc(markdown);
    const firstH1 = toc.find((e) => e.level === 1);
    if (firstH1 && firstH1.text) return firstH1.text;
  }
  return path.replace(/\.(md|markdown)$/i, '');
}
