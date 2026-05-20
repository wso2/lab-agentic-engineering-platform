import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { MdEditor } from './MdEditor.js';
import { useEditorStorage } from './hooks/useEditorStorage.js';

const meta = {
  title: 'Components/MdEditor',
  component: MdEditor,
  parameters: {
    layout: 'padded',
  },
  argTypes: {
    value: { control: 'text' },
    defaultValue: { control: 'text' },
    placeholder: { control: 'text' },
    readOnly: { control: 'boolean' },
    showToolbar: { control: 'boolean' },
    autoFocus: { control: 'boolean' },
    minHeight: { control: { type: 'number', min: 50, max: 800 } },
    maxHeight: { control: { type: 'number', min: 100, max: 800 } },
  },
} satisfies Meta<typeof MdEditor>;

export default meta;
type Story = StoryObj<typeof meta>;

const SAMPLE_MARKDOWN = `# Project Overview

This is a **sample document** demonstrating the WYSIWYG editor capabilities.

## Features

- Bold, *italic*, and ~~strikethrough~~ text
- Headings at multiple levels
- Bullet and ordered lists

## Getting Started

1. Install the package
2. Import the component
3. Start editing

> This is a blockquote that provides additional context about the project.

Here is some \`inline code\` and a code block:

\`\`\`
function hello() {
  console.log("Hello, world!");
}
\`\`\`

---

Visit [our docs](https://example.com) for more information.
`;

export const Default: Story = {};

export const WithInitialContent: Story = {
  args: {
    defaultValue: SAMPLE_MARKDOWN,
  },
};

export const Controlled: Story = {
  render: function ControlledStory() {
    const [markdown, setMarkdown] = useState(SAMPLE_MARKDOWN);
    return (
      <div style={{ display: 'flex', gap: '16px', height: '500px' }}>
        <div style={{ flex: 1 }}>
          <h3 style={{ margin: '0 0 8px' }}>WYSIWYG Editor</h3>
          <MdEditor
            value={markdown}
            onChange={setMarkdown}
            maxHeight={450}
          />
        </div>
        <div style={{ flex: 1 }}>
          <h3 style={{ margin: '0 0 8px' }}>Raw Markdown Output</h3>
          <textarea
            readOnly
            value={markdown}
            style={{
              width: '100%',
              height: '450px',
              fontFamily: 'monospace',
              fontSize: '13px',
              padding: '12px',
              border: '1px solid #e0e0e0',
              borderRadius: '6px',
              resize: 'none',
              boxSizing: 'border-box',
            }}
          />
        </div>
      </div>
    );
  },
};

export const ReadOnly: Story = {
  args: {
    value: SAMPLE_MARKDOWN,
    readOnly: true,
  },
};

export const NoToolbar: Story = {
  args: {
    defaultValue: '# Keyboard shortcuts only\n\nTry **Ctrl+B** for bold, *Ctrl+I* for italic.',
    showToolbar: false,
  },
};

export const CustomToolbarGroups: Story = {
  args: {
    defaultValue: 'Only text-style and lists toolbar groups are shown.',
    toolbarGroups: ['text-style', 'lists'],
  },
};

export const MarkdownRoundtrip: Story = {
  render: function RoundtripStory() {
    const [markdown, setMarkdown] = useState(SAMPLE_MARKDOWN);
    return (
      <div style={{ display: 'flex', gap: '16px', height: '500px' }}>
        <div style={{ flex: 1 }}>
          <h3 style={{ margin: '0 0 8px' }}>Raw Markdown (editable)</h3>
          <textarea
            value={markdown}
            onChange={(e) => setMarkdown(e.target.value)}
            style={{
              width: '100%',
              height: '450px',
              fontFamily: 'monospace',
              fontSize: '13px',
              padding: '12px',
              border: '1px solid #e0e0e0',
              borderRadius: '6px',
              resize: 'none',
              boxSizing: 'border-box',
            }}
          />
        </div>
        <div style={{ flex: 1 }}>
          <h3 style={{ margin: '0 0 8px' }}>WYSIWYG Editor</h3>
          <MdEditor
            value={markdown}
            onChange={setMarkdown}
            maxHeight={450}
          />
        </div>
      </div>
    );
  },
};

export const WithPlaceholder: Story = {
  args: {
    placeholder: 'Enter your project specification here...',
  },
};

export const WithPersistence: Story = {
  args: { defaultValue: '' },
  render: function PersistenceStory() {
    const storage = useEditorStorage({ storageKey: 'storybook-md-editor-demo' });

    return (
      <div>
        <div
          style={{
            display: 'flex',
            gap: '8px',
            alignItems: 'center',
            marginBottom: '12px',
          }}
        >
          <button
            type="button"
            onClick={storage.undo}
            disabled={!storage.canUndo}
            style={{
              padding: '6px 14px',
              border: '1px solid #ccc',
              borderRadius: '4px',
              background: storage.canUndo ? '#fff' : '#f5f5f5',
              color: storage.canUndo ? '#333' : '#aaa',
              cursor: storage.canUndo ? 'pointer' : 'default',
              fontSize: '13px',
            }}
          >
            Undo
          </button>
          <button
            type="button"
            onClick={storage.redo}
            disabled={!storage.canRedo}
            style={{
              padding: '6px 14px',
              border: '1px solid #ccc',
              borderRadius: '4px',
              background: storage.canRedo ? '#fff' : '#f5f5f5',
              color: storage.canRedo ? '#333' : '#aaa',
              cursor: storage.canRedo ? 'pointer' : 'default',
              fontSize: '13px',
            }}
          >
            Redo
          </button>
          <button
            type="button"
            onClick={storage.clear}
            style={{
              padding: '6px 14px',
              border: '1px solid #e0b0b0',
              borderRadius: '4px',
              background: '#fff',
              color: '#d32f2f',
              cursor: 'pointer',
              fontSize: '13px',
            }}
          >
            Clear Storage
          </button>
          <span style={{ fontSize: '12px', color: '#888', marginLeft: '8px' }}>
            Content persists across page reloads. Try editing, then refresh.
          </span>
        </div>
        <MdEditor
          value={storage.value}
          onChange={storage.onChange}
          placeholder="Start typing — your content will be auto-saved..."
        />
      </div>
    );
  },
};
