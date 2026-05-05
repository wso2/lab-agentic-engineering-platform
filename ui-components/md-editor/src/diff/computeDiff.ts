import { diffWords } from 'diff';
import type { JSONContent } from '@tiptap/react';

/** Node types that contain other blocks (not inline text). */
const CONTAINER_TYPES = new Set([
  'bulletList',
  'orderedList',
  'blockquote',
  'listItem',
  'doc',
]);

/**
 * Extract plain text from a node's content tree.
 */
function extractText(node: JSONContent): string {
  if (node.type === 'text') return node.text ?? '';
  if (!node.content) return '';
  return node.content.map(extractText).join('');
}

/**
 * Add a diff mark to a text node, preserving existing marks.
 */
function addMark(node: JSONContent, markType: string): JSONContent {
  const existingMarks = node.marks ?? [];
  return {
    ...node,
    marks: [...existingMarks, { type: markType }],
  };
}

/**
 * Mark all text nodes inside a node tree with a diff mark (recursive).
 */
function markBlock(block: JSONContent, markType: string): JSONContent {
  if (block.type === 'text') {
    return addMark(block, markType);
  }
  if (!block.content) return block;
  return {
    ...block,
    content: block.content.map((child) => markBlock(child, markType)),
  };
}

/**
 * Compute word-level inline diff between two leaf blocks (paragraph, heading, etc.).
 * Returns a new block with diffAdded/diffRemoved marks on changed text spans.
 */
function diffLeafBlock(
  oldBlock: JSONContent,
  newBlock: JSONContent,
): JSONContent {
  const oldText = extractText(oldBlock);
  const newText = extractText(newBlock);

  if (oldText === newText) return newBlock;

  const changes = diffWords(oldText, newText);

  const content: JSONContent[] = [];
  for (const change of changes) {
    if (!change.value) continue;

    if (change.removed) {
      content.push(addMark({ type: 'text', text: change.value }, 'diffRemoved'));
    } else if (change.added) {
      content.push(addMark({ type: 'text', text: change.value }, 'diffAdded'));
    } else {
      content.push({ type: 'text', text: change.value });
    }
  }

  return {
    ...newBlock,
    content: content.length > 0 ? content : undefined,
  };
}

/**
 * Align two arrays of blocks using text fingerprints.
 * Returns pairs of [oldBlock | null, newBlock | null].
 */
function alignBlocks(
  oldBlocks: JSONContent[],
  newBlocks: JSONContent[],
): Array<[JSONContent | null, JSONContent | null]> {
  const oldTexts = oldBlocks.map(extractText);
  const newTexts = newBlocks.map(extractText);

  const result: Array<[JSONContent | null, JSONContent | null]> = [];

  let oi = 0;
  let ni = 0;

  while (oi < oldBlocks.length && ni < newBlocks.length) {
    if (oldTexts[oi] === newTexts[ni]) {
      result.push([oldBlocks[oi], newBlocks[ni]]);
      oi++;
      ni++;
    } else {
      const newMatchIdx = newTexts.indexOf(oldTexts[oi], ni);
      const oldMatchIdx = oldTexts.indexOf(newTexts[ni], oi);

      if (
        newMatchIdx !== -1 &&
        (oldMatchIdx === -1 || newMatchIdx - ni <= oldMatchIdx - oi)
      ) {
        while (ni < newMatchIdx) {
          result.push([null, newBlocks[ni]]);
          ni++;
        }
      } else if (oldMatchIdx !== -1) {
        while (oi < oldMatchIdx) {
          result.push([oldBlocks[oi], null]);
          oi++;
        }
      } else {
        result.push([oldBlocks[oi], newBlocks[ni]]);
        oi++;
        ni++;
      }
    }
  }

  while (oi < oldBlocks.length) {
    result.push([oldBlocks[oi], null]);
    oi++;
  }

  while (ni < newBlocks.length) {
    result.push([null, newBlocks[ni]]);
    ni++;
  }

  return result;
}

/**
 * Diff two blocks recursively. Container nodes (lists, blockquotes) have their
 * children aligned and diffed. Leaf nodes (paragraph, heading, codeBlock) get
 * word-level inline diffs.
 */
function diffBlock(
  oldBlock: JSONContent,
  newBlock: JSONContent,
): JSONContent {
  const oldText = extractText(oldBlock);
  const newText = extractText(newBlock);

  // Identical content and type — unchanged
  if (oldText === newText && oldBlock.type === newBlock.type) {
    return newBlock;
  }

  // Different node types — show old as removed, new as added
  if (oldBlock.type !== newBlock.type) {
    // Return a wrapper that won't work at this level — caller handles this
    // by checking type mismatch before calling diffBlock
    return newBlock;
  }

  // Container node — recurse into children
  if (CONTAINER_TYPES.has(newBlock.type ?? '')) {
    const oldChildren = oldBlock.content ?? [];
    const newChildren = newBlock.content ?? [];
    const aligned = alignBlocks(oldChildren, newChildren);
    const mergedChildren: JSONContent[] = [];

    for (const [oldChild, newChild] of aligned) {
      if (oldChild && newChild) {
        if (oldChild.type !== newChild.type) {
          mergedChildren.push(markBlock(oldChild, 'diffRemoved'));
          mergedChildren.push(markBlock(newChild, 'diffAdded'));
        } else {
          mergedChildren.push(diffBlock(oldChild, newChild));
        }
      } else if (oldChild) {
        mergedChildren.push(markBlock(oldChild, 'diffRemoved'));
      } else if (newChild) {
        mergedChildren.push(markBlock(newChild, 'diffAdded'));
      }
    }

    return {
      ...newBlock,
      content: mergedChildren.length > 0 ? mergedChildren : undefined,
    };
  }

  // Leaf node — word-level inline diff
  return diffLeafBlock(oldBlock, newBlock);
}

/**
 * Compute a merged diff document from two parsed JSON documents.
 * Returns a new JSONContent document with diffAdded/diffRemoved marks.
 */
export function computeDiffDocument(
  oldDoc: JSONContent,
  newDoc: JSONContent,
): JSONContent {
  const oldBlocks = oldDoc.content ?? [];
  const newBlocks = newDoc.content ?? [];

  const aligned = alignBlocks(oldBlocks, newBlocks);
  const mergedContent: JSONContent[] = [];

  for (const [oldBlock, newBlock] of aligned) {
    if (oldBlock && newBlock) {
      if (oldBlock.type !== newBlock.type) {
        mergedContent.push(markBlock(oldBlock, 'diffRemoved'));
        mergedContent.push(markBlock(newBlock, 'diffAdded'));
      } else {
        mergedContent.push(diffBlock(oldBlock, newBlock));
      }
    } else if (oldBlock) {
      mergedContent.push(markBlock(oldBlock, 'diffRemoved'));
    } else if (newBlock) {
      mergedContent.push(markBlock(newBlock, 'diffAdded'));
    }
  }

  return {
    type: 'doc',
    content: mergedContent,
  };
}
