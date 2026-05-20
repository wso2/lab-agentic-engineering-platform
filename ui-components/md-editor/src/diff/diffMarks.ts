import { Mark, mergeAttributes } from '@tiptap/react';

export const DiffAdded = Mark.create({
  name: 'diffAdded',

  renderHTML({ HTMLAttributes }) {
    return ['ins', mergeAttributes({ class: 'diff-added' }, HTMLAttributes), 0];
  },
});

export const DiffRemoved = Mark.create({
  name: 'diffRemoved',

  renderHTML({ HTMLAttributes }) {
    return ['del', mergeAttributes({ class: 'diff-removed' }, HTMLAttributes), 0];
  },
});
