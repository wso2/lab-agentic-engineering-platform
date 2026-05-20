import type {
  PlanItem,
  PlanIssue,
  SlimDesignComponent,
  ExistingTaskSummary,
} from "./schema.js";

// Diff context the validator needs to enforce ADDED-coverage and the
// "empty-plan-allowed" rule for incremental mode. Pre-computed by the BFF
// (`task_diff.go`) and shipped over the wire.
export type DiffContext = {
  // Components added in the design vs the baseline batch.
  added: string[];
  // Components whose contract changed in a way that demands fresh work
  // (openapi changed OR dependsOn changed). Used together with `added` to
  // compute coverage in incremental mode.
  contractAffectedModified: string[];
  // Components removed vs baseline. Not load-bearing for validation, kept
  // for diagnostic context.
  removed: string[];
};

export type ValidatePlanInput = {
  items: PlanItemWithTempId[];
  design: SlimDesignComponent[];
  // Existing tasks across the project, used for dependsOn cross-references.
  // Filtered to non-rejected on the BFF before sending.
  existingTasks: ExistingTaskSummary[];
  mode: "fresh" | "incremental";
  diff?: DiffContext;
};

export type PlanItemWithTempId = PlanItem & { tempId: string };

export function validatePlan(input: ValidatePlanInput): PlanIssue[] {
  const issues: PlanIssue[] = [];
  const designNames = new Set(input.design.map((c) => c.name));
  const existingNonRejectedTitles = new Set(
    input.existingTasks.map((t) => t.title),
  );
  const titlesInBatch = new Set(input.items.map((i) => i.title));

  // 1. componentName exists in current design.
  for (const item of input.items) {
    if (!designNames.has(item.componentName)) {
      issues.push({
        tempId: item.tempId,
        code: "unknown-component",
        componentName: item.componentName,
      });
    }
  }

  // 2. Title uniqueness within batch.
  const seenTitles = new Map<string, string>(); // title -> first tempId
  for (const item of input.items) {
    const prior = seenTitles.get(item.title);
    if (prior !== undefined) {
      issues.push({
        code: "title-collision",
        title: item.title,
        firstTempId: prior,
        duplicateTempId: item.tempId,
      });
    } else {
      seenTitles.set(item.title, item.tempId);
    }
  }

  // 3. dependsOn resolves within batch or to existing non-rejected task.
  for (const item of input.items) {
    for (const dep of item.dependsOn) {
      if (titlesInBatch.has(dep)) continue;
      if (existingNonRejectedTitles.has(dep)) continue;
      issues.push({
        tempId: item.tempId,
        code: "dangling-dep",
        dep,
      });
    }
  }

  // 4. No cycles in dependsOn (within batch + existing non-rejected).
  // Build a directed graph keyed by title; nodes outside the batch are
  // terminal (we only walk batch-owned outbound edges).
  const adj: Record<string, string[]> = {};
  for (const item of input.items) {
    adj[item.title] = item.dependsOn.filter((d) => titlesInBatch.has(d));
  }
  const WHITE = 0,
    GRAY = 1,
    BLACK = 2;
  const color: Record<string, number> = {};
  for (const t of Object.keys(adj)) color[t] = WHITE;
  const cycleTitles = new Set<string>();
  function dfs(node: string, stack: string[]): void {
    color[node] = GRAY;
    stack.push(node);
    for (const next of adj[node] ?? []) {
      if (color[next] === GRAY) {
        const idx = stack.indexOf(next);
        if (idx >= 0) for (const n of stack.slice(idx)) cycleTitles.add(n);
      } else if (color[next] === WHITE) {
        dfs(next, stack);
      }
    }
    stack.pop();
    color[node] = BLACK;
  }
  for (const node of Object.keys(adj)) if (color[node] === WHITE) dfs(node, []);
  if (cycleTitles.size > 0) {
    for (const t of cycleTitles) {
      const tempId = seenTitles.get(t);
      issues.push({ tempId, code: "depends-on-cycle", title: t });
    }
  }

  // 5. Empty-plan rules.
  if (input.items.length === 0) {
    if (input.mode === "fresh") {
      issues.push({ code: "empty-plan-fresh" });
    } else if (input.mode === "incremental" && input.diff) {
      const trivial =
        input.diff.added.length === 0 &&
        input.diff.contractAffectedModified.length === 0;
      if (!trivial) {
        // Incremental with a non-trivial diff and zero plan items — flag the
        // missing-coverage entries explicitly so the model can self-correct.
        for (const name of input.diff.added) {
          issues.push({
            code: "missing-coverage",
            componentName: name,
            reason: "ADDED in design diff, no plan item targets it",
          });
        }
        for (const name of input.diff.contractAffectedModified) {
          issues.push({
            code: "missing-coverage",
            componentName: name,
            reason: "openapi or dependsOn changed, no plan item targets it",
          });
        }
      }
    }
  } else if (input.mode === "incremental" && input.diff) {
    // 6. ADDED-component coverage rule (incremental only — fresh mode is
    // covered by the "every component should have ≥ 1 task" prompt).
    const targetedComponents = new Set(input.items.map((i) => i.componentName));
    for (const name of input.diff.added) {
      if (!targetedComponents.has(name)) {
        issues.push({
          code: "missing-coverage",
          componentName: name,
          reason: "ADDED in design diff, no plan item targets it",
        });
      }
    }
  }

  return issues;
}
