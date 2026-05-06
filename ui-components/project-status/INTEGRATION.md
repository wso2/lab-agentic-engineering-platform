# Integration Guide — `@asdlc/project-status`

A drop-in React component that visualises the four-stage SDLC pipeline (Requirements → Architecture → Tasks → Deployment) as a polyline ridge banner. This guide covers wiring it into another package (typically `console/`).

If you're modifying the package itself, see `SPEC.md` instead.

---

## 1. When to use this component

Use it when you need a **single-glance project status banner** at the top of a page. Each stage shows: a name, a version (iteration count), a coloured state (done / active / pending / blocked), and a one-line headline. Clicking a node opens a side drawer with full detail (summary, metrics, artifacts, recent changes).

It is **purely presentational**. It does not fetch data and is agnostic to the data source — you pass it an array of `Stage` objects.

---

## 2. Install

This package lives in the `ui-components/*` workspace and is consumed via pnpm workspace symlinks. No publishing.

**Step 1** — add to the consumer's `package.json` dependencies:

```json
{
  "dependencies": {
    "@asdlc/project-status": "workspace:*"
  }
}
```

**Step 2** — from the repo root:

```bash
pnpm install
```

**Step 3** — confirm the peer deps are already in the consumer (in `console/` they are):

- `react ^19`
- `react-dom ^19`
- `@mui/material ^7`
- `@emotion/react ^11`
- `@emotion/styled ^11`
- `@wso2/oxygen-ui ^0.1`
- `@wso2/oxygen-ui-icons-react ^0.1`

If any are missing, add them. The `console/` workspace already has all of them.

---

## 3. Theme requirement

The component reads colours, typography, and shape tokens from `useTheme()`. The host app **must** be wrapped in an MUI `ThemeProvider` (or Oxygen UI's `OxygenUIThemeProvider`, which wraps MUI's). In the console this is already done in `console/src/main.tsx`:

```tsx
<OxygenUIThemeProvider theme={AcrylicOrangeTheme}>
  <CssBaseline />
  {/* app */}
</OxygenUIThemeProvider>
```

If a consumer lacks one, the component still renders using the default MUI theme but colours will not match the brand.

---

## 4. Quickstart — minimum integration

```tsx
import { ProjectStatusPolyline, type Stage } from '@asdlc/project-status';

const stages: Stage[] = [
  { id: 'req',  name: 'Requirements', iteration: 4, state: 'done',    headline: 'PRD locked' },
  { id: 'arch', name: 'Architecture', iteration: 3, state: 'done',    headline: 'Service map approved' },
  { id: 'task', name: 'Tasks & Code', iteration: 7, state: 'active',  headline: '34 / 52 tasks complete' },
  { id: 'dep',  name: 'Deployment',   iteration: 0, state: 'pending', headline: 'Awaiting tasks' },
];

export function MyOverview() {
  return <ProjectStatusPolyline stages={stages} />;
}
```

That's the whole integration. With `state: 'active'` set on the third stage, you'll see a pulsing ring animation. Click any node and a drawer opens — but with the minimal stage shape above the drawer is mostly empty. To populate it, fill in the optional fields (`summary`, `metrics`, `artifacts`, `changes`, `timestamp`, `duration`).

---

## 5. The `Stage` shape — what to fill in

| Field | Required | Drives |
|-------|----------|--------|
| `id` | yes | React key + drawer identity |
| `name` | yes | Label above the node |
| `iteration` | yes | Y-axis height (`0` = baseline). Caps at `maxIteration` (default 8) |
| `state` | yes | Colour + pulse: `'done' \| 'active' \| 'pending' \| 'blocked'` |
| `headline` | yes | Caption under the node |
| `summary` | no | Long paragraph in drawer |
| `metrics` | no | `[{ key, value }]` — up to 3 tiles in drawer |
| `artifacts` | no | `string[]` — file-name chips in drawer |
| `changes` | no | `string[]` — bullet list in drawer (titled "Activity" when state=`active`, else "Changes in v{n}") |
| `timestamp` | no | Free-form, e.g. `"2026-04-28 · 14:22"` or `"live"` |
| `duration` | no | Free-form, e.g. `"2d 4h"` |

If you don't need the drawer at all, keep just the required fields and pass `interactive={false}` (see §7).

---

## 6. Component props

```ts
interface ProjectStatusPolylineProps {
  stages: Stage[];                              // required
  onStageClick?: (stage: Stage) => void;        // delegates click; suppresses drawer
  interactive?: boolean;                        // default true; false = static banner
  mode?: 'light' | 'dark';                      // default = theme.palette.mode
  maxIteration?: number;                        // default 8; y-axis ceiling
}
```

`interactive`, `onStageClick`, and the internal drawer interact like this:

| `interactive` | `onStageClick` | Result |
|---|---|---|
| `true` (default) | unset | Click opens internal drawer |
| `true` | set | Click calls your handler; drawer suppressed |
| `false` | _ignored_ | Static — no click, no drawer, not focusable |

---

## 7. Common integration patterns

### 7a. Static banner (recommended for first integration)

No click, no drawer, no focus. Smallest surface area:

```tsx
<ProjectStatusPolyline stages={stages} interactive={false} />
```

### 7b. Default — click to open drawer

```tsx
<ProjectStatusPolyline stages={stages} />
```

The drawer is rendered inside the component using MUI's `Drawer` (anchor right, 420px wide). ESC and backdrop click both close it. No additional state needed in the consumer.

### 7c. Click delegates to your router

```tsx
import { useNavigate } from 'react-router-dom';

function Overview({ stages }) {
  const navigate = useNavigate();
  return (
    <ProjectStatusPolyline
      stages={stages}
      onStageClick={(s) => navigate(`/projects/${projectId}/${s.id}`)}
    />
  );
}
```

When `onStageClick` is set, the internal drawer is suppressed.

### 7d. You manage the drawer yourself

If you want a custom panel instead of the bundled `StageDrawer`, take over click handling. The drawer is also exported standalone so you can use it under your own control:

```tsx
import { ProjectStatusPolyline, StageDrawer, type Stage } from '@asdlc/project-status';
import { useState } from 'react';

function Overview({ stages }: { stages: Stage[] }) {
  const [picked, setPicked] = useState<Stage | null>(null);
  return (
    <>
      <ProjectStatusPolyline stages={stages} onStageClick={setPicked} />
      <StageDrawer stage={picked} open={picked !== null} onClose={() => setPicked(null)} />
    </>
  );
}
```

---

## 8. Mapping real project data to `Stage[]`

In this codebase the four stages map to existing artifacts:

| Stage | `iteration` source | `state` source | `headline` source |
|-------|--------------------|----------------|-------------------|
| `requirements` | `Spec.version` | `Spec.status` (`'approved'` → `done`, `'draft'` → `active`, missing → `pending`) | derive from `Spec.content` first line, or hard-code |
| `architecture` | `Design.version` | `Design.status` (`'approved'` → `done`, `'draft' \| 'generating'` → `active`, `'none'` → `pending`) | summary from `Design.overview` |
| `tasks` | `max(ComponentTask.iteration)` or simply count of merged PRs | aggregate of `ComponentTask.status` (any `in_progress` → `active`; all `merged \| deployed` → `done`; otherwise `pending`) | `${done}/${total} tasks complete` |
| `deployment` | count of successful deploys | aggregate of build/deploy state | "Rolled out to staging" / "Awaiting tasks" |

Sketch:

```ts
import type { Stage } from '@asdlc/project-status';
import type { Spec, Design, ComponentTask } from '../services/api';

function buildStages(spec: Spec | null, design: Design | null, tasks: ComponentTask[]): Stage[] {
  const tasksDone = tasks.filter(t => t.status === 'merged' || t.status === 'deployed').length;
  const tasksActive = tasks.some(t => t.status === 'in_progress');

  return [
    {
      id: 'requirements',
      name: 'Requirements',
      iteration: spec?.version ?? 0,
      state: spec?.status === 'approved' ? 'done' : spec ? 'active' : 'pending',
      headline: spec ? `Spec v${spec.version} ${spec.status}` : 'Not started',
    },
    {
      id: 'architecture',
      name: 'Architecture',
      iteration: design?.version ?? 0,
      state:
        design?.status === 'approved' ? 'done' :
        design?.status === 'draft' || design?.status === 'generating' ? 'active' :
        'pending',
      headline: design?.overview?.split('\n')[0] ?? 'Awaiting requirements',
    },
    {
      id: 'tasks',
      name: 'Tasks & Code',
      iteration: tasksDone,
      state: tasksActive ? 'active' : tasks.length > 0 && tasksDone === tasks.length ? 'done' : 'pending',
      headline: `${tasksDone} / ${tasks.length} tasks complete`,
    },
    {
      id: 'deployment',
      name: 'Deployment',
      iteration: 0, // TODO when build/deploy data is available
      state: 'pending',
      headline: 'Awaiting task completion',
    },
  ];
}
```

This is illustrative — match the actual API types you see in `console/src/services/api/types.ts`.

---

## 9. Layout & sizing

- The component renders an SVG with `viewBox="0 0 1400 420"` and `width="100%"`. It scales fluidly to its container width but keeps its aspect ratio.
- Wrap it in a container with the desired width. In `PageContent` from Oxygen UI, no extra wrapper is needed.
- The SVG has internal padding (`56px` left/right, `40px / 44px` top/bottom). Don't double-pad the parent.
- Below ~900px container width the headline text starts to crowd. If the consumer needs a sub-900px layout, wrap the component in a horizontally-scrolling container or hide it on mobile.

---

## 10. Dark mode

Auto-tracks `theme.palette.mode`. To override (e.g. force dark even though the host theme is light):

```tsx
<ProjectStatusPolyline stages={stages} mode="dark" />
```

---

## 11. Gotchas

- **`state: 'active'` triggers an indefinite SVG pulse animation.** Flip it to `'done'` (or `'pending'`) when the stage actually finishes; otherwise the ring keeps pulsing forever.
- **`iteration: 0` always renders flat at baseline**, regardless of state. The version label below shows `—` for any stage in `'pending'` state. For a "started but at v0" case, use `iteration: 1`.
- **The bundled drawer uses MUI's `Drawer`, not a portal you manage.** It mounts to `document.body`. If you have z-index battles with another modal/drawer, take over via `onStageClick` and render your own.
- **Stage IDs must be stable across renders.** They're React keys and drawer-open identity. Don't generate them with `Math.random()` inside the render.
- **Three stages is the minimum that looks good.** With one or two, the ridge geometry collapses. The component handles it (a single stage centres in the viewBox), but the visual loses its meaning.
- **The component does not memoise on `stages` identity.** If you build the array inline on every render and the list is large, wrap in `useMemo`. For the typical 3-6 stage case this doesn't matter.

---

## 12. Verifying your integration

1. `pnpm install` at repo root — confirms the workspace symlink is in place.
2. `pnpm typecheck` (or `tsc --noEmit`) inside the consumer — confirms `Stage[]` shape is correct.
3. Spin up the consumer's dev server and verify:
   - Banner renders at the expected width.
   - Active stage has a pulsing ring.
   - Clicking a node opens the drawer (or fires your handler).
   - ESC and backdrop click close the drawer.
   - Light / dark mode switch flips the banner palette.

For an isolated visual check before integrating, run the package's own Storybook:

```bash
cd ui-components/project-status && pnpm storybook   # http://localhost:6008
```

The `Default` story matches the exact look you'll get with the demo data.

---

## 13. Public exports

```ts
import {
  ProjectStatusPolyline,    // main component
  StageDrawer,              // standalone drawer (only needed for §7d)
  resolveStateMeta,         // (theme, state) => { dot, pillBg, pillText, label }
  type Stage,
  type StageState,          // 'done' | 'active' | 'pending' | 'blocked'
  type StageMetric,
  type ProjectStatusPolylineProps,
  type StageDrawerProps,
  type ResolvedStateMeta,
} from '@asdlc/project-status';
```

Anything not in this list is internal and may change without notice.
