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

// Lightweight skill projection shipped to the planner — name + description
// only. The planner uses these as context for splitting tasks but does not
// load the bodies (those go to the detail phase via TechLeadDetailItem).
export const AttachedSkillSummary = z.object({
  name: z.string(),
  description: z.string(),
});
export type AttachedSkillSummary = z.infer<typeof AttachedSkillSummary>;

// Resolved skill body shipped to the tech-lead detail phase. Full SKILL.md
// content for every skill attached to the project's design. The tech-lead
// inlines these under "Skills active for this project" with "MUST consult"
// framing — there is no two-tier split at this point because the architect
// has already attached only the relevant skills.
export const ResolvedSkill = z.object({
  name: z.string(),
  description: z.string(),
  body: z.string(),
});
export type ResolvedSkill = z.infer<typeof ResolvedSkill>;

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
  // Skills attached to this project — name + description only. The
  // planner uses these as context for splitting; bodies arrive in
  // TechLeadDetailItem.skillsResolved for the detail phase.
  attachedSkills: z.array(AttachedSkillSummary).optional(),
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
  // Full bodies of every skill attached to the project's design at
  // tech-lead detail time. The tech-lead inlines them in the user
  // prompt with "MUST consult" framing.
  skillsResolved: z.array(ResolvedSkill).optional(),
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
