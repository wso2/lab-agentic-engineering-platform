import StarterKit from '@tiptap/starter-kit';
import Link from '@tiptap/extension-link';
import Placeholder from '@tiptap/extension-placeholder';
import { Markdown } from '@tiptap/markdown';
import { Collaboration } from '@tiptap/extension-collaboration';
import { CollaborationCaret } from '@tiptap/extension-collaboration-caret';
import type { Extensions } from '@tiptap/react';
import type { Doc } from 'yjs';
import { DiffAdded, DiffRemoved } from '../diff/diffMarks.js';
import { DiffDecorations } from './diffDecorations.js';

export interface CollabConfig {
  ydoc: Doc;
  provider: { awareness: unknown };
  user: { name: string; color: string };
}

export function createExtensions(options: {
  placeholder?: string;
  includeDiffMarks?: boolean;
  getBaseMarkdown?: () => string | undefined;
  collab?: CollabConfig;
}): Extensions {
  const extensions: Extensions = [
    // StarterKit's link conflicts with our configured Link below; undoRedo
    // must be off when Y.js owns undo (Collaboration extension below).
    StarterKit.configure({
      link: false,
      ...(options.collab ? { undoRedo: false } : {}),
    }),
    Link.configure({
      openOnClick: false,
      HTMLAttributes: {
        rel: 'noopener noreferrer',
        target: '_blank',
      },
    }),
    Placeholder.configure({
      placeholder: options.placeholder ?? 'Write something...',
    }),
    Markdown,
  ];

  if (options.includeDiffMarks) {
    extensions.push(DiffAdded, DiffRemoved);
  }

  // The extension list is fixed at editor creation, so we always register
  // DiffDecorations with a getter — returning `undefined` makes it a no-op.
  extensions.push(
    DiffDecorations.configure({
      getBaseMarkdown: options.getBaseMarkdown ?? (() => undefined),
    }),
  );

  if (options.collab) {
    const { ydoc, provider, user } = options.collab;
    extensions.push(
      Collaboration.configure({ document: ydoc }),
      CollaborationCaret.configure({ provider, user }),
    );
  }

  return extensions;
}
