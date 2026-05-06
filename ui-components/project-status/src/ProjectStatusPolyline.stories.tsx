import type { Meta, StoryObj } from '@storybook/react-vite';
import { ThemeProvider, createTheme } from '@mui/material/styles';
import CssBaseline from '@mui/material/CssBaseline';
import { ProjectStatusPolyline } from './ProjectStatusPolyline.js';
import type { Stage } from './types.js';

const STAGES_DEMO: Stage[] = [
  {
    id: 'requirements',
    name: 'Requirements',
    iteration: 4,
    state: 'done',
    headline: 'PRD locked — 18 user stories, 6 success metrics',
    metrics: [
      { key: 'User stories', value: '18' },
      { key: 'Success metrics', value: '6' },
      { key: 'Stakeholders', value: '4' },
    ],
    artifacts: ['PRD.md', 'user-stories.yaml', 'acceptance-criteria.md'],
    timestamp: '2026-04-28 · 14:22',
    duration: '2d 4h',
    summary:
      'Final pass after stakeholder review. Removed scope around bulk-import (deferred to v2). Added two new metrics for activation funnel. All acceptance criteria are now testable.',
    changes: [
      'Removed: bulk CSV import (deferred)',
      'Added: activation funnel metrics',
      'Refined: payment flow acceptance criteria',
    ],
  },
  {
    id: 'architecture',
    name: 'Architecture',
    iteration: 3,
    state: 'done',
    headline: 'Service map approved — 5 services, Postgres + Redis',
    metrics: [
      { key: 'Services', value: '5' },
      { key: 'Data stores', value: '2' },
      { key: 'External APIs', value: '3' },
    ],
    artifacts: ['system-design.md', 'sequence-diagrams.mmd', 'data-model.dbml'],
    timestamp: '2026-04-30 · 09:10',
    duration: '1d 6h',
    summary:
      'Split monolith proposal into auth, billing, ingest, worker, and api-gateway. Chose Postgres for primary store, Redis for queue + session. Decision log captures the 3 alternatives evaluated.',
    changes: [
      'Switched: Kafka → Redis Streams (cost + ops)',
      'Added: api-gateway service for rate limiting',
      'Decided: tenancy model (shared schema, row-level)',
    ],
  },
  {
    id: 'tasks',
    name: 'Tasks & Code',
    iteration: 7,
    state: 'active',
    headline: '34 / 52 tasks complete · 2 agents coding now',
    metrics: [
      { key: 'Tasks done', value: '34/52' },
      { key: 'PRs open', value: '4' },
      { key: 'Tests passing', value: '218/220' },
    ],
    artifacts: ['tasklist.md', 'src/', 'tests/', '4 open PRs'],
    timestamp: '2026-05-06 · live',
    duration: '6d (running)',
    summary:
      'Backlog broken into 52 tasks across the 5 services. Two coding agents are currently working: one on billing webhooks, one on ingest retry logic. Two test failures under investigation in worker service.',
    changes: [
      'agent-α: implementing billing.webhooks (T-31)',
      'agent-β: ingest retry policy (T-34)',
      'failing: worker.dedupe.spec.ts (investigating)',
    ],
  },
  {
    id: 'deployment',
    name: 'Deployment',
    iteration: 0,
    state: 'pending',
    headline: 'Awaiting task completion',
    metrics: [
      { key: 'Environments', value: '0/3' },
      { key: 'Health checks', value: '—' },
      { key: 'Rollouts', value: '—' },
    ],
    artifacts: ['deploy.yaml (draft)', 'runbook.md (draft)'],
    timestamp: '—',
    duration: '—',
    summary:
      'Deployment plan drafted but blocked on Tasks stage. Targeting staging first, then 10% canary, then full rollout. Runbook auto-generated from architecture decisions.',
    changes: [],
  },
];

const STAGES_ALL_STATES: Stage[] = [
  { ...STAGES_DEMO[0], id: 'done', name: 'Done', state: 'done', iteration: 5 },
  { ...STAGES_DEMO[2], id: 'active', name: 'Active', state: 'active', iteration: 6 },
  {
    ...STAGES_DEMO[1],
    id: 'blocked',
    name: 'Blocked',
    state: 'blocked',
    iteration: 2,
    headline: 'Awaiting external API access',
  },
  { ...STAGES_DEMO[3], id: 'pending', name: 'Pending', state: 'pending', iteration: 0 },
];

const STAGES_ALL_DONE: Stage[] = STAGES_DEMO.map((s, i) => ({
  ...s,
  state: 'done' as const,
  iteration: [4, 3, 7, 2][i] ?? 1,
  headline: s.headline.replace('Awaiting task completion', 'Rolled out to all environments'),
}));

const STAGES_ALL_PENDING: Stage[] = STAGES_DEMO.map((s) => ({
  ...s,
  state: 'pending' as const,
  iteration: 0,
  headline: 'Not started',
  changes: [],
}));

const meta: Meta<typeof ProjectStatusPolyline> = {
  title: 'Components/ProjectStatusPolyline',
  component: ProjectStatusPolyline,
  parameters: {
    layout: 'fullscreen',
  },
  decorators: [
    (Story) => (
      <div style={{ padding: 32, background: '#f0eee9', minHeight: '100vh' }}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ProjectStatusPolyline>;

export const Default: Story = {
  args: {
    stages: STAGES_DEMO,
  },
};

export const AllStates: Story = {
  args: {
    stages: STAGES_ALL_STATES,
  },
};

export const AllComplete: Story = {
  args: {
    stages: STAGES_ALL_DONE,
  },
};

export const AllPending: Story = {
  args: {
    stages: STAGES_ALL_PENDING,
  },
};

const darkTheme = createTheme({ palette: { mode: 'dark' } });

export const DarkMode: Story = {
  args: {
    stages: STAGES_DEMO,
  },
  decorators: [
    (Story) => (
      <ThemeProvider theme={darkTheme}>
        <CssBaseline />
        <div style={{ padding: 32, background: '#0f0d0a', minHeight: '100vh' }}>
          <Story />
        </div>
      </ThemeProvider>
    ),
  ],
};

export const ClickHandlerOverride: Story = {
  args: {
    stages: STAGES_DEMO,
    onStageClick: (stage) => {
      // eslint-disable-next-line no-alert
      alert(`Clicked stage: ${stage.name} (v${stage.iteration})`);
    },
  },
};

export const NonInteractive: Story = {
  args: {
    stages: STAGES_DEMO,
    interactive: false,
  },
};
