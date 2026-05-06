# ProjectStatus Component Specification

## Overview

A hero-banner React component visualising the four stages of an agentic SDLC project (Requirements → Architecture → Tasks → Deployment) on a polyline ridge chart. Each node carries an iteration count (version), a state, and a one-line headline. Clicking a node opens a side drawer with full stage detail (summary, metrics, recent changes, artifacts).

The component is a pure presentational widget: it accepts a `stages` array as a prop and renders. It does not fetch data and is agnostic to the data source.

### Design Principles

1. **Stages in, SVG out** — caller supplies a fully-resolved `Stage[]`; the component owns layout, theming, and interaction.
2. **Inherit the host theme** — colours, typography, spacing all flow from the MUI / Oxygen UI `useTheme()` context. No bundled fonts.
3. **Semantic state colours** — `done` / `active` / `pending` / `blocked` map to `palette.success / primary / text.disabled / error`.
4. **Live "running" state has presence** — the active stage gets a pulsing SVG ring; everything else is static.
5. **Detail behind a click** — keeping the banner compact; full stage detail lives in a side drawer.

### Technology

- Plain React + SVG. No charting library.
- MUI `Drawer` for the detail panel (gets us slide-in animation, ESC/backdrop-close, focus trap for free).
- MUI `useTheme()` + `alpha()` helper for theme-aware colour resolution.

### Dependencies

Peer: `react ^19`, `react-dom ^19`, `@mui/material ^7`, `@emotion/react ^11`, `@emotion/styled ^11`, `@wso2/oxygen-ui ^0.1`, `@wso2/oxygen-ui-icons-react ^0.1`. No runtime dependencies.

---

## Component API

### `<ProjectStatusPolyline>` Props

```ts
interface ProjectStatusPolylineProps {
  /** Stages, left-to-right. Typically 3-6 entries; the design targets 4. */
  stages: Stage[];

  /** If provided, click on a stage delegates to this and the internal drawer is suppressed. */
  onStageClick?: (stage: Stage) => void;

  /** Whether stages are clickable. When false, the internal drawer is suppressed,
   *  `onStageClick` is ignored, and nodes render as static. Default: true. */
  interactive?: boolean;

  /** Override theme-derived dark/light mode. Defaults to theme.palette.mode. */
  mode?: 'light' | 'dark';

  /** Max iteration value for the y-axis scale; clamps node height. Default: 8. */
  maxIteration?: number;
}
```

### `Stage` shape

```ts
type StageState = 'done' | 'active' | 'pending' | 'blocked';

interface StageMetric { key: string; value: string; }

interface Stage {
  id: string;
  name: string;          // "Requirements"
  iteration: number;     // 0..N. 0 + state='pending' = baseline node.
  state: StageState;
  headline: string;      // one-line summary under the node
  summary?: string;      // longer paragraph in drawer
  metrics?: StageMetric[];   // up to 3 — shown as a grid in drawer
  artifacts?: string[];      // file/path chips
  changes?: string[];        // bullet list (titled "Activity" when state='active')
  timestamp?: string;        // free-form ("2026-04-28 · 14:22" or "live")
  duration?: string;         // free-form ("2d 4h", "6d (running)")
}
```

### Usage

```tsx
import { ProjectStatusPolyline, type Stage } from '@asdlc/project-status';

const stages: Stage[] = [/* ... */];

<ProjectStatusPolyline stages={stages} />

// Or take over click handling:
<ProjectStatusPolyline stages={stages} onStageClick={(s) => navigate(`/stages/${s.id}`)} />
```

### Public exports

- `ProjectStatusPolyline` — main component.
- `StageDrawer` — exported so consumers can drive the drawer themselves if they hook up `onStageClick`.
- Types: `Stage`, `StageState`, `StageMetric`, `ProjectStatusPolylineProps`, `StageDrawerProps`.

---

## Theming

The component reads tokens from MUI's `useTheme()`. Active palette (light vs dark) follows `theme.palette.mode` unless the `mode` prop is set.

| Token | Usage |
|-------|-------|
| `palette.background.paper` | Banner surface |
| `palette.text.primary` | Stage names, version numbers |
| `palette.text.secondary` | Headlines, drawer copy |
| `palette.text.disabled` | Pending state |
| `palette.divider` | Baseline + grid lines, borders |
| `palette.primary.main` | Ridge line, fill gradient, active state |
| `palette.success.main` | Done state |
| `palette.error.main` | Blocked state |
| `typography.fontFamily` | All text |
| `shape.borderRadius` | Banner corners |

State-colour resolution lives in `src/stateMeta.ts` and is the single source of truth.

---

## Architecture

```
src/
  index.ts                       # Public exports
  types.ts                       # Stage, StageState, StageMetric, prop types
  stateMeta.ts                   # resolveStateMeta(theme, state) -> { dot, pillBg, pillText }
  ProjectStatusPolyline.tsx      # SVG ridge with stage nodes
  StageDrawer.tsx                # MUI Drawer with stage detail
  ProjectStatusPolyline.stories.tsx
```

### Polyline geometry

- Internal SVG viewBox: 1400 × 420, scaled to container width via `width="100%"`.
- Node x = padX + (innerW * i) / (stages.length - 1).
- Node y = baselineY − norm × (baselineY − peakY), where `norm = state === 'pending' ? 0 : min(iteration / maxIteration, 1)`.
- Straight line segments between consecutive nodes (no smoothing).
- Filled area under the ridge using a top-down `linearGradient` from `primary.main` (with alpha) to transparent.
- Iteration grid ticks at v3 and v6 for visual reference.

### Active-state pulse

Two concentric `<animate>` rings inside the active node's `<circle>` element. SMIL animation — runs without JavaScript and is unaffected by React re-renders.

### Drawer

`<Drawer anchor="right" variant="temporary" open={...} onClose={...}>` wraps a fixed-width 420px panel with header (state pill + name + version/timestamp), summary paragraph, 3-column metrics grid, "Activity" / "Changes in v{n}" list, artifact chips, and a footer (duration + iteration). The Drawer handles ESC and backdrop click out of the box.

---

## Storybook Stories

| Story | Purpose |
|-------|---------|
| Default | The canonical four-stage Atlas demo (third stage `active`) — matches the design hero |
| AllStates | One stage per state (done / active / pending / blocked) — checks colour mapping |
| AllComplete | Every stage `done` — sanity-checks the no-pulse case |
| AllPending | Flat baseline, all "—" version labels — sanity-checks the zero-iteration path |
| DarkMode | Default story wrapped in a dark MUI `ThemeProvider` |

Stories live in `src/ProjectStatusPolyline.stories.tsx`. Demo data is inlined in the story file to keep the package self-contained.

Run with `pnpm storybook` (port 6008).

---

## Non-Functional Requirements

| Requirement | Target |
|-------------|--------|
| Renders without a host theme | Yes — falls back to MUI default theme tokens via `useTheme()` |
| Bundle size | < 10KB gzipped (component-only; MUI Drawer is a peer-dep cost) |
| Accessibility | Stage nodes are keyboard-focusable buttons inside the SVG; drawer has focus trap via MUI |
| Browser support | Chrome, Firefox, Safari, Edge (latest 2 versions) |

---

## Future Work

- Wire to real project data (Spec/Design versions + ComponentTask aggregates) when consumed in `ProjectOverviewPage`.
- Optional Deployment stage breakdown (per-environment chips).
- Smooth ridge curves as a prop (`shape: 'straight' | 'smoothed'`).
- Mini variant for sidebars / cards.
