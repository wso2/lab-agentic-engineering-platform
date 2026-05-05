import { diffWords } from 'diff';
import { Decoration } from '@tiptap/pm/view';
import type { Node as PmNode } from '@tiptap/pm/model';

/**
 * Collect top-level text blocks from a ProseMirror document.
 * For container nodes (lists, blockquotes), we recurse and collect
 * the leaf blocks (paragraphs, headings, etc.) with their positions.
 */
interface BlockInfo {
  text: string;
  from: number;
  to: number;
  node: PmNode;
}

function collectTextBlocks(doc: PmNode): BlockInfo[] {
  const blocks: BlockInfo[] = [];
  doc.descendants((node, pos) => {
    if (node.isTextblock) {
      blocks.push({
        text: node.textContent,
        from: pos + 1, // inside the node (after the opening token)
        to: pos + node.nodeSize - 1,
        node,
      });
      return false; // don't descend into textblocks
    }
    return true; // descend into container nodes
  });
  return blocks;
}

/**
 * Simple alignment of two string arrays by content.
 * Returns pairs of [oldIdx | -1, newIdx | -1].
 */
function alignTexts(
  oldTexts: string[],
  newTexts: string[],
): Array<[number, number]> {
  const result: Array<[number, number]> = [];
  let oi = 0;
  let ni = 0;

  while (oi < oldTexts.length && ni < newTexts.length) {
    if (oldTexts[oi] === newTexts[ni]) {
      result.push([oi, ni]);
      oi++;
      ni++;
    } else {
      const newMatch = newTexts.indexOf(oldTexts[oi], ni);
      const oldMatch = oldTexts.indexOf(newTexts[ni], oi);

      if (newMatch !== -1 && (oldMatch === -1 || newMatch - ni <= oldMatch - oi)) {
        while (ni < newMatch) {
          result.push([-1, ni]);
          ni++;
        }
      } else if (oldMatch !== -1) {
        while (oi < oldMatch) {
          result.push([oi, -1]);
          oi++;
        }
      } else {
        result.push([oi, ni]);
        oi++;
        ni++;
      }
    }
  }

  while (oi < oldTexts.length) {
    result.push([oi, -1]);
    oi++;
  }
  while (ni < newTexts.length) {
    result.push([-1, ni]);
    ni++;
  }

  return result;
}

/**
 * Create a DOM element for deleted text (widget decoration).
 */
function createDeletedWidget(text: string): HTMLElement {
  const el = document.createElement('del');
  el.className = 'diff-removed-widget';
  el.textContent = text;
  el.contentEditable = 'false';
  return el;
}

/**
 * Compute ProseMirror decorations by diffing base text blocks against
 * the current document's text blocks.
 *
 * @param baseTexts Array of text content from each base block
 * @param currentDoc The live ProseMirror document
 * @returns Array of ProseMirror Decoration objects
 */
export function computeDecorations(
  baseTexts: string[],
  currentDoc: PmNode,
): Decoration[] {
  const currentBlocks = collectTextBlocks(currentDoc);
  const currentTexts = currentBlocks.map((b) => b.text);

  const aligned = alignTexts(baseTexts, currentTexts);
  const decorations: Decoration[] = [];

  for (const [oldIdx, newIdx] of aligned) {
    if (oldIdx >= 0 && newIdx >= 0) {
      // Both blocks exist — compute inline diff
      const baseText = baseTexts[oldIdx];
      const block = currentBlocks[newIdx];
      const currentText = block.text;

      if (baseText === currentText) continue; // unchanged

      const changes = diffWords(baseText, currentText);

      // Step 1: Coalesce consecutive remove/add pairs into groups so that
      // all deleted text renders before all added text at each position.
      interface ChangeGroup {
        removed: string;
        added: string;
        addedLen: number;
      }
      const rawGroups: Array<ChangeGroup | { unchanged: string }> = [];
      let pending: ChangeGroup | null = null;

      const flushPending = () => {
        if (pending) {
          rawGroups.push(pending);
          pending = null;
        }
      };

      for (const change of changes) {
        if (!change.value) continue;

        if (change.removed) {
          if (!pending) pending = { removed: '', added: '', addedLen: 0 };
          pending.removed += change.value;
        } else if (change.added) {
          if (!pending) pending = { removed: '', added: '', addedLen: 0 };
          pending.added += change.value;
          pending.addedLen += change.value.length;
        } else {
          flushPending();
          rawGroups.push({ unchanged: change.value });
        }
      }
      flushPending();

      // Step 2: Merge nearby change groups separated by short unchanged gaps.
      // If an unchanged segment between two change groups has <= MAX_GAP_WORDS words,
      // absorb it into a single larger change group.
      const MAX_GAP_WORDS = 3;
      const groups: Array<ChangeGroup | { unchanged: string }> = [];

      for (let i = 0; i < rawGroups.length; i++) {
        const current = rawGroups[i];

        // If this is a change group, try to merge with the next change group(s)
        if (!('unchanged' in current)) {
          const merged: ChangeGroup = { ...current };

          while (i + 2 < rawGroups.length) {
            const gap = rawGroups[i + 1];
            const next = rawGroups[i + 2];

            // Check: gap is a short unchanged segment, next is a change group
            if (
              'unchanged' in gap &&
              !('unchanged' in next) &&
              gap.unchanged.trim().split(/\s+/).length <= MAX_GAP_WORDS
            ) {
              // Absorb the gap into both removed and added
              merged.removed += gap.unchanged + next.removed;
              merged.added += gap.unchanged + next.added;
              merged.addedLen += gap.unchanged.length + next.addedLen;
              i += 2; // skip gap and next
            } else {
              break;
            }
          }

          groups.push(merged);
        } else {
          groups.push(current);
        }
      }

      // Render coalesced groups as decorations
      let offset = block.from;

      for (const group of groups) {
        if ('unchanged' in group) {
          offset += group.unchanged.length;
        } else {
          // Render all removed text first as a single widget
          if (group.removed) {
            decorations.push(
              Decoration.widget(offset, () => createDeletedWidget(group.removed), {
                side: -1,
                key: `del-${offset}-${group.removed.slice(0, 10)}`,
              }),
            );
          }
          // Then render all added text as a single inline decoration
          if (group.addedLen > 0) {
            decorations.push(
              Decoration.inline(offset, offset + group.addedLen, { class: 'diff-added' }),
            );
            offset += group.addedLen;
          }
        }
      }
    } else if (oldIdx >= 0) {
      // Block was deleted — show as widget at the nearest position
      const baseText = baseTexts[oldIdx];
      // Find the position to insert the widget:
      // If there are current blocks, insert near the corresponding position
      const insertPos = newIdx >= 0 ? currentBlocks[newIdx].from : (currentBlocks.length > 0 ? currentBlocks[currentBlocks.length - 1].to : 1);
      decorations.push(
        Decoration.widget(
          Math.min(insertPos, currentDoc.content.size),
          () => createDeletedWidget(baseText),
          { side: 1, key: `del-block-${oldIdx}` },
        ),
      );
    } else if (newIdx >= 0) {
      // Block was added — highlight entire block
      const block = currentBlocks[newIdx];
      if (block.from < block.to) {
        decorations.push(
          Decoration.inline(block.from, block.to, { class: 'diff-added' }),
        );
      }
    }
  }

  return decorations;
}

/**
 * Extract text content from each text block in a parsed markdown string.
 * Uses a temporary DOM-less approach: parse the markdown JSONContent and
 * extract text from each block.
 */
export function extractBaseTexts(baseJson: { content?: Array<{ type?: string; content?: unknown[] }> }): string[] {
  const texts: string[] = [];

  function extractFromNode(node: Record<string, unknown>): void {
    if (node.type === 'text') {
      return; // handled by parent
    }

    // Check if this is a textblock (has inline content)
    const content = node.content as Array<Record<string, unknown>> | undefined;
    if (!content) return;

    const isTextBlock = content.some((c) => c.type === 'text');
    if (isTextBlock) {
      // Extract text from this textblock
      let text = '';
      for (const child of content) {
        if (child.type === 'text') {
          text += (child.text as string) ?? '';
        }
      }
      texts.push(text);
      return;
    }

    // Recurse into container nodes
    for (const child of content) {
      extractFromNode(child);
    }
  }

  if (baseJson.content) {
    for (const block of baseJson.content) {
      extractFromNode(block as Record<string, unknown>);
    }
  }

  return texts;
}
