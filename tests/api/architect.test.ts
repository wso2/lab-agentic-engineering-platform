/**
 * End-to-end API tests for the architect agent.
 *
 * These hit a real agents-service over a kubectl port-forward and consume
 * actual SSE streams from a real Anthropic call. They cost money and minutes
 * per run, so they're only enabled when ARCHITECT_TESTS=1.
 *
 * Pre-req:
 *   kubectl port-forward -n dp-wso2cloud-app-factory-development-bad5f211 \
 *     svc/app-factory-agents-service 13400:3400
 *
 * Run:
 *   ARCHITECT_TESTS=1 npx vitest run api/architect.test.ts
 */

import { describe, it, expect, beforeAll } from 'vitest';
import { callArchitect } from '../helpers/architect';
import { collectFrames, type SseFrame } from '../helpers/sse';

const ENABLED = process.env.ARCHITECT_TESTS === '1';
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

const TODO_PLUS_NOTIFICATION_SPEC = `${TODO_SPEC}

Additional requirement:
- When a user marks a todo as done, send a notification (email-style for now,
  log-only is fine). Notifications are handled by a dedicated service.`;

type Frame = SseFrame;

function eventTypes(frames: Frame[]): string[] {
  return frames.map((f) => f.type);
}

function findFinish(frames: Frame[]): Frame | undefined {
  return frames.find((f) => f.type === 'data-finish');
}

function findError(frames: Frame[]): Frame | undefined {
  return frames.find((f) => f.type === 'error');
}

type FinishedDesign = {
  overview: string;
  requirements: string[];
  components: Array<{
    name: string;
    componentType: string;
    componentAgentInstructions: string;
    openAPISpec: string;
    [key: string]: unknown;
  }>;
};

function getDesign(finish: Frame): FinishedDesign {
  const data = finish.data as { design: FinishedDesign };
  return data.design;
}

describeMaybe('Architect — fresh todo app', () => {
  let frames: Frame[];

  beforeAll(async () => {
    const res = await callArchitect({
      projectName: 'todo-fresh',
      spec: TODO_SPEC,
    });
    expect(res.status, await safeBody(res, 200)).toBe(200);
    frames = await collectFrames(res);
  }, 240_000);

  it('emits no error frame', () => {
    expect(findError(frames)).toBeUndefined();
  });

  it('emits data-finish exactly once', () => {
    const count = frames.filter((f) => f.type === 'data-finish').length;
    expect(count).toBe(1);
  });

  it('emits component-added events before any spec-updating events', () => {
    const types = eventTypes(frames);
    const lastAdded = types.lastIndexOf('data-component-added');
    const firstUpdating = types.indexOf('data-component-spec-updating');
    expect(lastAdded).toBeGreaterThanOrEqual(0);
    if (firstUpdating !== -1) {
      // Cards must be visible before spinners start.
      expect(lastAdded).toBeLessThan(firstUpdating);
    }
  });

  it('produces at least 2 components', () => {
    const finish = findFinish(frames);
    expect(finish).toBeDefined();
    const design = getDesign(finish!);
    expect(design.components.length).toBeGreaterThanOrEqual(2);
  });

  it('every component has a non-empty openAPISpec at finish', () => {
    const design = getDesign(findFinish(frames)!);
    for (const c of design.components) {
      expect(c.openAPISpec.length, `${c.name} openAPISpec`).toBeGreaterThan(0);
    }
  });

  it('every service component has GET /health in its spec', () => {
    const design = getDesign(findFinish(frames)!);
    for (const c of design.components) {
      if (c.componentType !== 'service') continue;
      expect(c.openAPISpec, `${c.name} /health`).toMatch(/\/health/);
    }
  });
});

describeMaybe('Architect — incremental: add notification feature', () => {
  let freshDesign: FinishedDesign;
  let frames: Frame[];

  beforeAll(async () => {
    // Pass 1: generate fresh, capture as previousDesign.
    const r1 = await callArchitect({
      projectName: 'todo-incremental',
      spec: TODO_SPEC,
    });
    expect(r1.status).toBe(200);
    const f1 = await collectFrames(r1);
    expect(findError(f1)).toBeUndefined();
    freshDesign = getDesign(findFinish(f1)!);

    // Pass 2: feed previousDesign + extended spec.
    const r2 = await callArchitect({
      projectName: 'todo-incremental',
      spec: TODO_PLUS_NOTIFICATION_SPEC,
      previousDesign: freshDesign,
    });
    expect(r2.status).toBe(200);
    frames = await collectFrames(r2);
  }, 480_000);

  it('emits no error frame', () => {
    expect(findError(frames)).toBeUndefined();
  });

  it('finalizes successfully', () => {
    expect(findFinish(frames)).toBeDefined();
  });

  it('untouched components do NOT receive a spec-updating event', () => {
    // The incremental rule: components whose intended spec is unchanged are
    // preserved verbatim and the model should not re-emit set_openapi for them.
    const updatingNames = frames
      .filter((f) => f.type === 'data-component-spec-updating')
      .map((f) => (f.data as { name: string }).name);

    // todo-web is unaffected by adding a notification service. It should be
    // preserved verbatim — no spec-updating for it.
    if (freshDesign.components.some((c) => c.name === 'todo-web')) {
      expect(updatingNames, 'todo-web preserved verbatim').not.toContain(
        'todo-web',
      );
    }
  });

  it('preserved-untouched components keep openAPISpec byte-equal', () => {
    const updated = getDesign(findFinish(frames)!);
    for (const prev of freshDesign.components) {
      // Skip components that the incremental run might intentionally invalidate.
      if (prev.name !== 'todo-web') continue;
      const after = updated.components.find((c) => c.name === prev.name);
      if (!after) return; // intentionally removed — that's its own thing
      expect(after.openAPISpec, `${prev.name} preserved verbatim`).toBe(
        prev.openAPISpec,
      );
    }
  });

  it('introduces at least one new component', () => {
    const updated = getDesign(findFinish(frames)!);
    const prevNames = new Set(freshDesign.components.map((c) => c.name));
    const newComponents = updated.components.filter(
      (c) => !prevNames.has(c.name),
    );
    expect(newComponents.length).toBeGreaterThanOrEqual(1);
  });
});

describeMaybe('Architect — wholesale rewrite of one component', () => {
  let freshDesign: FinishedDesign;
  let frames: Frame[];

  beforeAll(async () => {
    const r1 = await callArchitect({
      projectName: 'todo-rewrite',
      spec: TODO_SPEC,
    });
    expect(r1.status).toBe(200);
    const f1 = await collectFrames(r1);
    freshDesign = getDesign(findFinish(f1)!);

    // Force a wholesale rewrite of the backend by changing its language.
    const rewriteSpec = `${TODO_SPEC}

IMPORTANT REWRITE: The backend service must now be implemented in Ballerina
instead of Go. Do not preserve the old Go-based backend — it should be
replaced wholesale (remove, then re-add) so the destructive intent is
visible to reviewers.`;

    const r2 = await callArchitect({
      projectName: 'todo-rewrite',
      spec: rewriteSpec,
      previousDesign: freshDesign,
    });
    expect(r2.status).toBe(200);
    frames = await collectFrames(r2);
  }, 480_000);

  it('emits no error frame', () => {
    expect(findError(frames)).toBeUndefined();
  });

  it('finalizes successfully', () => {
    expect(findFinish(frames)).toBeDefined();
  });

  it('emits component-removed followed by component-added for the rewritten component', () => {
    // Find any name that appears in both removed and added events.
    const removedNames = frames
      .filter((f) => f.type === 'data-component-removed')
      .map((f) => (f.data as { name: string }).name);
    const addedNames = frames
      .filter((f) => f.type === 'data-component-added')
      .map((f) => (f.data as { component: { name: string } }).component.name);

    const overlap = removedNames.filter((n) => addedNames.includes(n));
    expect(overlap.length, 'expected at least one remove+add pair').toBeGreaterThanOrEqual(1);

    // Order check: for each overlapping name, the removed event must come
    // before the added event.
    for (const name of overlap) {
      const types = frames.map((f) => {
        if (f.type === 'data-component-removed') {
          return (f.data as { name: string }).name === name ? 'removed' : '';
        }
        if (f.type === 'data-component-added') {
          return (f.data as { component: { name: string } }).component.name ===
            name
            ? 'added'
            : '';
        }
        return '';
      });
      const removeIdx = types.indexOf('removed');
      const addIdx = types.indexOf('added');
      expect(removeIdx, `${name}: remove before add`).toBeLessThan(addIdx);
    }
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
