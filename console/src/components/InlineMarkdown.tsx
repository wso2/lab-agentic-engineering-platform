import { Box } from '@wso2/oxygen-ui';

/**
 * Lightweight read-only markdown renderer for streaming task bodies.
 *
 * Why not react-markdown: streaming re-renders happen on every coalesced
 * delta (~250ms). A pure-string transform with regex is cheap, dependency-
 * free, and good enough for the 5-section task bodies we render. Bodies
 * never include arbitrary user-authored HTML — they come from our own
 * detail-phase prompt which constrains structure.
 *
 * Supported subset (in order of precedence, applied per line/block):
 *   - Code fences   ```lang … ```      → <pre><code>
 *   - Headings      ## / ### / ####    → <h3>/<h4>/<h5>
 *   - Bullet lists  - foo              → <li> grouped into <ul>
 *   - Inline code   `foo`              → <code>
 *   - Bold          **foo**            → <strong>
 *   - Paragraphs    everything else, blank-line-separated
 *
 * Emojis, raw HTML, links, images, and tables are intentionally NOT
 * supported — they don't appear in the templated detail-phase output and
 * would inflate this component significantly.
 */
export function InlineMarkdown({ body }: { body: string }) {
  const blocks = parseBlocks(body);
  return (
    <Box
      sx={{
        // Tight typography for in-card markdown; matches the ProjectSpecPage
        // render style.
        '& h3': {
          fontSize: '0.95rem',
          fontWeight: 600,
          mt: 1.5,
          mb: 0.5,
          color: 'text.primary',
        },
        '& h4, & h5': {
          fontSize: '0.85rem',
          fontWeight: 600,
          mt: 1.25,
          mb: 0.5,
          color: 'text.primary',
        },
        '& p': {
          fontSize: '0.86rem',
          lineHeight: 1.55,
          my: 0.75,
          color: 'text.primary',
        },
        '& ul': {
          fontSize: '0.86rem',
          pl: 2.5,
          my: 0.5,
        },
        '& li': { mb: 0.25, lineHeight: 1.55 },
        '& code': {
          fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
          fontSize: '0.78rem',
          bgcolor: 'action.hover',
          px: 0.5,
          py: 0.125,
          borderRadius: 0.5,
        },
        '& pre': {
          bgcolor: 'action.hover',
          p: 1,
          borderRadius: 1,
          overflowX: 'auto',
          fontSize: '0.78rem',
          my: 0.75,
        },
        '& pre code': { bgcolor: 'transparent', p: 0 },
        '& strong': { fontWeight: 600 },
      }}
    >
      {blocks.map((b, i) => renderBlock(b, i))}
    </Box>
  );
}

type Block =
  | { kind: 'pre'; lang: string; text: string }
  | { kind: 'heading'; level: 3 | 4 | 5; text: string }
  | { kind: 'list'; items: string[] }
  | { kind: 'p'; text: string };

function parseBlocks(body: string): Block[] {
  const blocks: Block[] = [];
  const lines = body.split('\n');
  let i = 0;
  let listItems: string[] | null = null;

  const flushList = () => {
    if (listItems && listItems.length > 0) {
      blocks.push({ kind: 'list', items: listItems });
    }
    listItems = null;
  };

  while (i < lines.length) {
    const line = lines[i];

    // Code fence
    if (line.trimStart().startsWith('```')) {
      flushList();
      const lang = line.trim().slice(3);
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith('```')) {
        codeLines.push(lines[i]);
        i++;
      }
      i++; // skip closing fence (or EOF)
      blocks.push({ kind: 'pre', lang, text: codeLines.join('\n') });
      continue;
    }

    // Heading
    const h = /^(#{2,5})\s+(.+)$/.exec(line);
    if (h) {
      flushList();
      const level = Math.min(5, Math.max(3, h[1].length)) as 3 | 4 | 5;
      blocks.push({ kind: 'heading', level, text: h[2] });
      i++;
      continue;
    }

    // Bullet list (- or *)
    const bullet = /^\s*[-*]\s+(.+)$/.exec(line);
    if (bullet) {
      if (!listItems) listItems = [];
      listItems.push(bullet[1]);
      i++;
      continue;
    }

    // Blank line — close list / paragraph
    if (line.trim() === '') {
      flushList();
      i++;
      continue;
    }

    // Paragraph: collect contiguous non-special lines.
    flushList();
    const paraLines = [line];
    i++;
    while (
      i < lines.length &&
      lines[i].trim() !== '' &&
      !lines[i].trimStart().startsWith('```') &&
      !/^(#{2,5})\s+/.test(lines[i]) &&
      !/^\s*[-*]\s+/.test(lines[i])
    ) {
      paraLines.push(lines[i]);
      i++;
    }
    blocks.push({ kind: 'p', text: paraLines.join(' ') });
  }
  flushList();
  return blocks;
}

function renderInline(s: string): React.ReactNode[] {
  // **bold** and `code`. Single regex pass, no nesting.
  const parts: React.ReactNode[] = [];
  const re = /(\*\*[^*]+\*\*|`[^`]+`)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    if (m.index > last) parts.push(s.slice(last, m.index));
    const tok = m[0];
    if (tok.startsWith('**')) {
      parts.push(<strong key={parts.length}>{tok.slice(2, -2)}</strong>);
    } else {
      parts.push(<code key={parts.length}>{tok.slice(1, -1)}</code>);
    }
    last = m.index + tok.length;
  }
  if (last < s.length) parts.push(s.slice(last));
  return parts;
}

function renderBlock(b: Block, key: number): React.ReactNode {
  switch (b.kind) {
    case 'pre':
      return (
        <pre key={key}>
          <code>{b.text}</code>
        </pre>
      );
    case 'heading': {
      if (b.level === 3) return <h3 key={key}>{renderInline(b.text)}</h3>;
      if (b.level === 4) return <h4 key={key}>{renderInline(b.text)}</h4>;
      return <h5 key={key}>{renderInline(b.text)}</h5>;
    }
    case 'list':
      return (
        <ul key={key}>
          {b.items.map((it, j) => (
            <li key={j}>{renderInline(it)}</li>
          ))}
        </ul>
      );
    case 'p':
      return <p key={key}>{renderInline(b.text)}</p>;
  }
}
