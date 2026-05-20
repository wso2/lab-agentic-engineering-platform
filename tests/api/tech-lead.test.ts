/**
 * End-to-end API tests for the tech-lead agent (plan + detail routes).
 *
 * These hit a real agents-service over a kubectl port-forward and consume
 * actual SSE streams from a real Anthropic call. They cost money and minutes
 * per run, so they're only enabled when TECH_LEAD_TESTS=1.
 *
 * Pre-req:
 *   kubectl port-forward -n dp-wso2cloud-app-factory-development-bad5f211 \
 *     svc/app-factory-agents-service 13400:3400
 *
 * Run:
 *   TECH_LEAD_TESTS=1 npx vitest run api/tech-lead.test.ts
 */

import { describe, it, expect, beforeAll } from 'vitest';
import {
  callTechLeadPlan,
  callTechLeadDetail,
  type SlimComponent,
} from '../helpers/tech-lead';
import { collectFrames, type SseFrame } from '../helpers/sse';

const ENABLED = process.env.TECH_LEAD_TESTS === '1';
const describeMaybe = ENABLED ? describe : describe.skip;

const TODO_SPEC = `Build a simple TODO web application.

Functional requirements:
- Users can view a list of all todos
- Users can add a new todo with a title
- Users can mark a todo as done or undone
- Users can delete a todo
- Each todo has: id, title, done flag

Architecture:
- A web frontend (TypeScript + React)
- A backend service (Go) exposing REST endpoints under /todos
- In-memory storage is fine for the prototype`;

const FRESH_DESIGN: SlimComponent[] = [
  { name: 'todo-api', componentType: 'service', language: 'Go', dependsOn: [] },
  {
    name: 'todo-web',
    componentType: 'web-app',
    language: 'TypeScript / React',
    dependsOn: ['todo-api'],
  },
];

function eventTypes(frames: SseFrame[]): string[] {
  return frames.map((f) => f.type);
}

function findError(frames: SseFrame[]): SseFrame | undefined {
  return frames.find((f) => f.type === 'error');
}

function findPlanComplete(frames: SseFrame[]): SseFrame | undefined {
  return frames.find((f) => f.type === 'data-plan-complete');
}

describeMaybe('TechLead/Plan — fresh-mode 2-component design', () => {
  let frames: SseFrame[];

  beforeAll(async () => {
    const res = await callTechLeadPlan({
      projectName: 'todo-fresh',
      spec: TODO_SPEC,
      slimDesign: FRESH_DESIGN,
      mode: 'fresh',
      diff: { added: [], contractAffectedModified: [], removed: [] },
    });
    expect(res.status, await safeBody(res, 200)).toBe(200);
    frames = await collectFrames(res);
  }, 120_000);

  it('emits no error frame', () => {
    expect(findError(frames)).toBeUndefined();
  });

  it('emits at least 2 plan-item events', () => {
    const items = frames.filter((f) => f.type === 'data-plan-item');
    expect(items.length).toBeGreaterThanOrEqual(2);
  });

  it('every plan item has a tempId, title, componentName, rationale', () => {
    const items = frames.filter((f) => f.type === 'data-plan-item');
    for (const it of items) {
      const d = it.data as Record<string, unknown>;
      expect(typeof d.tempId).toBe('string');
      expect(typeof d.title).toBe('string');
      expect(typeof d.componentName).toBe('string');
      expect(typeof d.rationale).toBe('string');
      expect(Array.isArray(d.dependsOn)).toBe(true);
    }
  });

  it('plan items target only known components', () => {
    const items = frames.filter((f) => f.type === 'data-plan-item');
    const knownNames = new Set(FRESH_DESIGN.map((c) => c.name));
    for (const it of items) {
      const name = (it.data as { componentName: string }).componentName;
      expect(knownNames.has(name), `unknown component ${name}`).toBe(true);
    }
  });

  it('emits data-plan-complete after all items', () => {
    const finishIdx = frames.findIndex(
      (f) => f.type === 'data-plan-complete',
    );
    expect(finishIdx).toBeGreaterThan(-1);
    // Every plan-item should appear before plan-complete.
    const itemIdxs = frames
      .map((f, i) => (f.type === 'data-plan-item' ? i : -1))
      .filter((i) => i >= 0);
    for (const i of itemIdxs) {
      expect(i).toBeLessThan(finishIdx);
    }
  });

  it('plan-complete payload includes the items list', () => {
    const pc = findPlanComplete(frames);
    expect(pc).toBeDefined();
    const d = pc!.data as { items: unknown[] };
    expect(Array.isArray(d.items)).toBe(true);
    expect(d.items.length).toBeGreaterThanOrEqual(2);
  });

  it('every component is covered by at least one task', () => {
    const items = frames.filter((f) => f.type === 'data-plan-item');
    const targeted = new Set(
      items.map((it) => (it.data as { componentName: string }).componentName),
    );
    for (const c of FRESH_DESIGN) {
      expect(targeted.has(c.name), `${c.name} not targeted`).toBe(true);
    }
  });
});

describeMaybe('TechLead/Plan — validator: empty plan in fresh mode → error', () => {
  // We can't reliably force the model to emit an empty array, but we can
  // exercise the empty-plan-fresh code path by sending an empty design — the
  // model has nothing to plan against. If the model still produces tasks
  // (anti-hallucination doesn't always fire), unknown-component should
  // trigger.
  let frames: SseFrame[];

  beforeAll(async () => {
    const res = await callTechLeadPlan({
      projectName: 'empty-fresh',
      spec: '(intentionally empty)',
      slimDesign: [],
      mode: 'fresh',
      diff: { added: [], contractAffectedModified: [], removed: [] },
    });
    expect(res.status).toBe(200);
    frames = await collectFrames(res);
  }, 60_000);

  it('emits an error frame (validator rejection of empty plan or invalid component)', () => {
    const err = findError(frames);
    expect(err, `events: ${eventTypes(frames).join(',')}`).toBeDefined();
    const data = err!.data as { scope?: string };
    expect(data?.scope).toBe('plan');
  });
});

describeMaybe('TechLead/Detail — short body for one task', () => {
  let frames: SseFrame[];

  beforeAll(async () => {
    const taskId = 'task-detail-1';
    const designSlice = JSON.stringify({
      name: 'todo-api',
      componentType: 'service',
      language: 'Go',
      dependsOn: [],
      openAPISpec: 'openapi: 3.0.0\npaths:\n  /todos:\n    get:\n      responses: {"200":{"description":"ok"}}\n',
    });
    const res = await callTechLeadDetail({
      projectName: 'todo-fresh',
      spec: TODO_SPEC,
      items: [
        {
          taskId,
          componentName: 'todo-api',
          title: 'Implement todo-api',
          rationale: 'Core CRUD service backing the todo list.',
          designSlice,
          depSummaries: [],
          existingTitlesForComponent: [],
        },
      ],
    });
    expect(res.status, await safeBody(res, 200)).toBe(200);
    frames = await collectFrames(res);
  }, 120_000);

  it('emits at least one body-delta frame', () => {
    const deltas = frames.filter((f) => f.type === 'data-task-body-delta');
    expect(deltas.length).toBeGreaterThanOrEqual(1);
  });

  it('emits exactly one body-complete for the task', () => {
    const completes = frames.filter((f) => f.type === 'data-task-body-complete');
    expect(completes.length).toBe(1);
  });

  it('body-complete payload includes a non-empty markdown body with required sections', () => {
    const c = frames.find((f) => f.type === 'data-task-body-complete');
    expect(c).toBeDefined();
    const d = c!.data as { taskId: string; body: string };
    expect(d.taskId).toBe('task-detail-1');
    expect(d.body.length).toBeGreaterThan(50);
    expect(d.body).toContain('## What');
    expect(d.body).toContain('## Acceptance criteria');
    expect(d.body).toContain('## Implementation notes');
    expect(d.body).toContain('## Contracts');
    expect(d.body).toContain('## Dependencies');
  });

  it('body does NOT inline the OpenAPI YAML', () => {
    const c = frames.find((f) => f.type === 'data-task-body-complete');
    const d = c!.data as { body: string };
    // The system prompt forbids inlining; the body should reference design.json.
    expect(d.body).toMatch(/design\.json/i);
  });
});

async function safeBody(res: Response, expected: number): Promise<string> {
  if (res.status === expected) return '';
  try {
    return `expected ${expected}, got ${res.status}: ${await res.text()}`;
  } catch {
    return `expected ${expected}, got ${res.status}`;
  }
}
