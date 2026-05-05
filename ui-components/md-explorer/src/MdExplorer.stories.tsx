import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { MdExplorer } from './MdExplorer.js';
import type { FileMap } from './types.js';

const meta = {
  title: 'Components/MdExplorer',
  component: MdExplorer,
  parameters: { layout: 'padded' },
  argTypes: {
    sidebarWidth: { control: { type: 'number', min: 180, max: 500 } },
    minHeight: { control: { type: 'number', min: 200, max: 900 } },
    maxHeight: { control: { type: 'number', min: 200, max: 900 } },
    searchPlaceholder: { control: 'text' },
  },
} satisfies Meta<typeof MdExplorer>;

export default meta;
type Story = StoryObj<typeof meta>;

const SHIPPING_PLAN = `# Plan on Shipping Agent Skills

## Shipping Agent Skills

We ship agent skills via the following channels.

## Installation Options

### Option 1: Via WSO2 Product Catalogue

Enterprise customers pull skills from the product catalogue bundled with WSO2 IDP.

#### Version management

Versions align with the surrounding product release train.

### Option 2: Claude Code Plugins

Developer-facing distribution as Claude Code plugins, installable via \`claude plugins\`.

#### Version management

Semver per plugin, independent of product releases.

### Option 3: npx skills add

Lightweight one-off distribution via \`npx skills add <name>\`.

#### Version management

Published to npm; same semver rules as any library package.

## Repo Structure

### File Descriptions

Every directory has a README describing its purpose.

### Root Level

Top-level config, build, and entry points.

### Plugin Level

Each plugin directory is self-contained with its own manifest.

## References

- WSO2 IDP docs
- Claude Code plugin spec
`;

const DATA_MODEL = `# Data Model

## Entities

### User

The canonical user record.

### Project

Top-level workspace for a set of components.

## Relationships

Users own projects; projects contain components.
`;

const ARCH_NOTES = `# Architecture Notes

## Goals

- Composable
- Observable

## Non-Goals

- Monolithic coupling
`;

const SAMPLE_FILES: FileMap = {
  'Plan on Shipping Agent Skills.md': SHIPPING_PLAN,
  'Data Model.md': DATA_MODEL,
  'Architecture Notes.md': ARCH_NOTES,
  'TODO.md': '# TODO\n\n- Write tests\n- Ship it\n',
};

export const Default: Story = {
  args: {
    defaultFiles: SAMPLE_FILES,
    defaultActivePath: 'Plan on Shipping Agent Skills.md',
    minHeight: 600,
  },
};

export const Controlled: Story = {
  args: {},
  render: function ControlledStory() {
    const [files, setFiles] = useState<FileMap>(SAMPLE_FILES);
    const [active, setActive] = useState<string | null>('Plan on Shipping Agent Skills.md');
    const [lastEdit, setLastEdit] = useState<{ path: string; md: string } | null>(null);

    return (
      <div style={{ display: 'flex', gap: 16, flexDirection: 'column' }}>
        <MdExplorer
          files={files}
          activePath={active}
          onActivePathChange={setActive}
          onFileChange={(path, md) => {
            setLastEdit({ path, md });
            setFiles((f) => ({ ...f, [path]: md }));
          }}
          onAddFile={() => {
            let n = 1;
            while (`Untitled ${n}.md` in files) n++;
            const name = `Untitled ${n}.md`;
            setFiles((f) => ({ ...f, [name]: '' }));
            setActive(name);
            return name;
          }}
          onRename={(oldPath, newPath) => {
            setFiles((f) => {
              const { [oldPath]: v, ...rest } = f;
              return { ...rest, [newPath]: v };
            });
            setActive((a) => (a === oldPath ? newPath : a));
          }}
          onDelete={(path) => {
            setFiles((f) => {
              const { [path]: _, ...rest } = f;
              return rest;
            });
          }}
          minHeight={600}
        />
        <div
          style={{
            fontFamily: 'monospace',
            fontSize: 12,
            padding: 8,
            border: '1px solid #e0e0e0',
            borderRadius: 4,
            background: '#fafafa',
          }}
        >
          <div><strong>Active:</strong> {active ?? '(none)'}</div>
          <div><strong>Last onFileChange:</strong> {lastEdit ? lastEdit.path : '(none)'}</div>
          <div><strong>Files:</strong> {Object.keys(files).join(', ') || '(empty)'}</div>
        </div>
      </div>
    );
  },
};

export const TocNavigation: Story = {
  args: {
    defaultFiles: { 'Plan on Shipping Agent Skills.md': SHIPPING_PLAN },
    defaultActivePath: 'Plan on Shipping Agent Skills.md',
    minHeight: 600,
  },
  parameters: {
    docs: {
      description: {
        story:
          'Click any ToC entry in the sidebar to scroll the editor to that heading. The ToC updates live as you edit headings in the document.',
      },
    },
  },
};

export const ReadOnlyActions: Story = {
  args: {
    defaultFiles: SAMPLE_FILES,
    defaultActivePath: 'Data Model.md',
    editorProps: { readOnly: true },
    minHeight: 600,
    // No onRename / onDelete / onAddFile -> no kebab, no "+" button.
  },
  parameters: {
    docs: {
      description: {
        story:
          'Omit onRename, onDelete, and onAddFile for a purely navigational explorer. ToC clicks still work; the editor is read-only.',
      },
    },
  },
};

export const WithAddFile: Story = {
  args: {
    defaultFiles: SAMPLE_FILES,
    defaultActivePath: 'TODO.md',
    onAddFile: () => undefined,
    minHeight: 600,
  },
  parameters: {
    docs: {
      description: {
        story:
          'The "+" button shows in the sidebar header when onAddFile is passed. In uncontrolled mode, returning undefined lets MdExplorer auto-generate an "Untitled N.md" file.',
      },
    },
  },
};

export const RenameAndDelete: Story = {
  args: {
    defaultFiles: SAMPLE_FILES,
    defaultActivePath: 'Architecture Notes.md',
    onAddFile: () => undefined,
    minHeight: 600,
  },
  parameters: {
    docs: {
      description: {
        story:
          'Click the kebab (⋮) on the active file to rename or delete. Renaming to an existing filename is rejected inline.',
      },
    },
  },
};

export const EmptyState: Story = {
  args: {
    defaultFiles: {},
    onAddFile: () => undefined,
    emptyState: (
      <div>
        <div style={{ fontSize: 16, marginBottom: 4 }}>No documents yet</div>
        <div style={{ fontSize: 12, color: '#888' }}>
          Click the <strong>+</strong> in the sidebar to create one.
        </div>
      </div>
    ),
    minHeight: 600,
  },
};

export const LiveTocUpdate: Story = {
  args: {
    defaultFiles: {
      'Live.md':
        '# Start here\n\nEdit this document — add `## New section` and watch the sidebar update.\n',
    },
    defaultActivePath: 'Live.md',
    minHeight: 600,
  },
  parameters: {
    docs: {
      description: {
        story:
          'The ToC is parsed from the current editor buffer. Try adding or removing a heading to see the sidebar update immediately.',
      },
    },
  },
};
