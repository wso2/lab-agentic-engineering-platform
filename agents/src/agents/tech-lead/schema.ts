import { z } from "zod";

// Slim component shape passed to the planner. Mirrors DesignComponent without
// the OpenAPI YAML payload — the planner reasons about topology and roles, not
// contracts. Detail phase gets the full design entry per task.
export const SlimDesignComponent = z.object({
  name: z.string(),
  componentType: z.string(),
  language: z.string(),
  dependsOn: z.array(z.string()),
});

export type SlimDesignComponent = z.infer<typeof SlimDesignComponent>;

// Existing task summary shipped to the planner so it can avoid duplicating
// already-planned work. Status is included verbatim so the model can reason
// about which tasks are real vs draft.
export const ExistingTaskSummary = z.object({
  issueNumber: z.number().int().optional(),
  title: z.string(),
  componentName: z.string(),
  status: z.string(),
});

export type ExistingTaskSummary = z.infer<typeof ExistingTaskSummary>;

// PlanItem — one row of the planner's output. The planner does NOT emit
// tempId; the route assigns "p-0", "p-1", … sequentially and pairs them with
// the seal-rule emitter (route layer).
export const PlanItemSchema = z.object({
  componentName: z
    .string()
    .describe("Must match a component name in the current architecture."),
  title: z
    .string()
    .describe(
      "GitHub issue title. Must be unique within this batch (used as the dependsOn key).",
    ),
  rationale: z
    .string()
    .describe("One sentence explaining why this task exists."),
  dependsOn: z
    .array(z.string())
    .describe(
      "Titles of other plans in this batch this depends on. Omit titles of already-merged tasks.",
    ),
});

export type PlanItem = z.infer<typeof PlanItemSchema>;

// The full plan is a non-empty (in fresh mode) array of PlanItem.
export const PlanArraySchema = z.array(PlanItemSchema);

// Phase 1 input.
export const TechLeadPlanInput = z.object({
  projectName: z.string(),
  spec: z.string(),
  slimDesign: z.array(SlimDesignComponent),
  // Pre-formatted unified diff (BFF computes; agent only renders).
  specDiff: z.string().optional(),
  designDiff: z.string().optional(),
  existingTasks: z.array(ExistingTaskSummary).optional(),
  mode: z.enum(["fresh", "incremental"]),
});

export type TechLeadPlanInput = z.infer<typeof TechLeadPlanInput>;

// Phase 2 input — one entry per task surviving GH issue creation.
export const TechLeadDetailItem = z.object({
  taskId: z.string().describe("Persisted DB UUID; round-tripped on the wire."),
  componentName: z.string(),
  title: z.string(),
  rationale: z.string(),
  // The component's design entry assembled from
  // `specs/design/components/<name>/{design.md,openapi.yaml}` and shipped
  // as a JSON slice for the prompt. Includes openAPISpec, appPath,
  // buildpack, etc. — the model only renders references, never inlines YAML.
  designSlice: z.string(),
  // Slim summaries (name/type/language) of dependsOn components.
  depSummaries: z.array(SlimDesignComponent),
  // Titles + status of prior tasks targeting the same component, for context.
  existingTitlesForComponent: z.array(
    z.object({ title: z.string(), status: z.string() }),
  ),
});

export type TechLeadDetailItem = z.infer<typeof TechLeadDetailItem>;

export const TechLeadDetailInput = z.object({
  projectName: z.string(),
  spec: z.string(),
  items: z.array(TechLeadDetailItem),
});

export type TechLeadDetailInput = z.infer<typeof TechLeadDetailInput>;

// Validator output — one structured issue per problem found in the plan.
export type PlanIssue = {
  tempId?: string;
  code: string;
  [key: string]: unknown;
};
