import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { MdDiffViewer } from './MdDiffViewer.js';
import { MdEditor } from './MdEditor.js';

const meta = {
  title: 'Components/MdDiffViewer',
  component: MdDiffViewer,
  parameters: {
    layout: 'padded',
  },
} satisfies Meta<typeof MdDiffViewer>;

export default meta;
type Story = StoryObj<typeof meta>;

export const BasicDiff: Story = {
  args: {
    oldMarkdown: `# Project Overview

This is the original project description with some basic details.

## Goals

- Build a fast application
- Support multiple users
- Ensure data security`,
    newMarkdown: `# Project Overview

This is the updated project description with comprehensive details.

## Goals

- Build a fast and reliable application
- Support multiple users and roles
- Ensure data security`,
  },
};

export const AddedContent: Story = {
  args: {
    oldMarkdown: `# Features

- User authentication
- Dashboard`,
    newMarkdown: `# Features

- User authentication
- Dashboard
- Real-time notifications
- Export to PDF

## Getting Started

Run \`npm install\` to get started.`,
  },
};

export const RemovedContent: Story = {
  args: {
    oldMarkdown: `# API Reference

## Authentication

All requests require a bearer token.

## Deprecated Endpoints

These endpoints will be removed in v3:

- \`GET /api/v1/legacy\`
- \`POST /api/v1/old-format\`

## Current Endpoints

- \`GET /api/v2/users\`
- \`POST /api/v2/projects\``,
    newMarkdown: `# API Reference

## Authentication

All requests require a bearer token.

## Current Endpoints

- \`GET /api/v2/users\`
- \`POST /api/v2/projects\``,
  },
};

export const MixedChanges: Story = {
  args: {
    oldMarkdown: `# Release Notes v1.0

## Bug Fixes

- Fixed login timeout issue
- Resolved memory leak in dashboard
- Fixed CSS alignment on mobile

## Known Issues

- Performance degradation with large datasets
- Occasional sync failures

## Contributors

Thanks to the core team for their work.`,
    newMarkdown: `# Release Notes v1.1

## New Features

- Added dark mode support
- Introduced keyboard shortcuts

## Bug Fixes

- Fixed login timeout issue
- Resolved memory leak in dashboard
- Fixed intermittent API connection errors

## Known Issues

- Performance degradation with large datasets

## Contributors

Thanks to the core team and community contributors for their work.`,
  },
};

export const NoChanges: Story = {
  args: {
    oldMarkdown: `# Hello World

This content is **exactly the same** in both versions.

- Item one
- Item two`,
    newMarkdown: `# Hello World

This content is **exactly the same** in both versions.

- Item one
- Item two`,
  },
};

export const InteractiveDiff: Story = {
  args: { oldMarkdown: '', newMarkdown: '' },
  render: function InteractiveDiffStory() {
    const originalMd = '# Project Overview\n\nThis is the original project description with some basic details.\n\n## Goals\n\n- Build a fast application\n- Support multiple users\n- Ensure data security';
    const [newMd, setNewMd] = useState(originalMd);

    return (
      <div>
        <p style={{ fontSize: '13px', color: '#666', margin: '0 0 12px' }}>
          Edit the content below — additions are highlighted in green, deletions shown with red strikethrough.
        </p>
        <MdEditor
          value={newMd}
          onChange={setNewMd}
          baseMarkdown={originalMd}
          minHeight={400}
        />
      </div>
    );
  },
};
