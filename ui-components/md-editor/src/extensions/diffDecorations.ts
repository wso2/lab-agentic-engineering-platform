import { Extension } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import { DecorationSet } from '@tiptap/pm/view';
import { computeDecorations, extractBaseTexts } from '../diff/computeDecorations.js';

export const diffDecorationsKey = new PluginKey('diffDecorations');

export interface DiffDecorationsOptions {
  /**
   * Returns the base markdown to diff against (or `undefined` to disable).
   * Read via getter so callers can swap in async-loaded values without
   * recreating the editor. Dispatch a `diffDecorationsKey` setMeta to force a
   * rebuild after the underlying value changes.
   */
  getBaseMarkdown: () => string | undefined;
}

export const DiffDecorations = Extension.create<DiffDecorationsOptions>({
  name: 'diffDecorations',

  addOptions() {
    return {
      getBaseMarkdown: () => undefined,
    };
  },

  addProseMirrorPlugins() {
    const editor = this.editor;
    const getBaseMarkdown = this.options.getBaseMarkdown;

    // Re-parse only when the base string actually changes, not per transaction.
    let cachedBase: string | undefined;
    let cachedTexts: string[] = [];

    const baseTextsForCurrent = (): string[] => {
      const base = getBaseMarkdown();
      if (!base) {
        cachedBase = undefined;
        cachedTexts = [];
        return cachedTexts;
      }
      if (base === cachedBase) return cachedTexts;
      cachedBase = base;
      cachedTexts = editor.markdown ? extractBaseTexts(editor.markdown.parse(base)) : [];
      return cachedTexts;
    };

    return [
      new Plugin({
        key: diffDecorationsKey,
        state: {
          init(_, state) {
            const texts = baseTextsForCurrent();
            if (texts.length === 0) return DecorationSet.empty;
            return DecorationSet.create(state.doc, computeDecorations(texts, state.doc));
          },
          apply(tr, oldSet, _oldState, newState) {
            const baseChanged = !!tr.getMeta(diffDecorationsKey);
            if (!tr.docChanged && !baseChanged) return oldSet.map(tr.mapping, tr.doc);
            const texts = baseTextsForCurrent();
            if (texts.length === 0) return DecorationSet.empty;
            return DecorationSet.create(newState.doc, computeDecorations(texts, newState.doc));
          },
        },
        props: {
          decorations(state) {
            return diffDecorationsKey.getState(state);
          },
        },
      }),
    ];
  },
});
