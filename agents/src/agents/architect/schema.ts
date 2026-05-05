import { z } from "zod";

// SlimComponent — shape metadata only, no openAPISpec. The architect emits
// these via add_component / set_* tools so the UI can render component cards
// before the (large) OpenAPI YAML has streamed.
export const SlimComponent = z.object({
  name: z
    .string()
    .describe("Lowercase kebab-case component name, e.g. 'user-api'"),
  componentType: z
    .enum(["service", "web-app", "scheduled-task"])
    .describe(
      "Component type: 'web-app' for frontends, 'service' for backend APIs, 'scheduled-task' for cron/batch jobs",
    ),
  language: z
    .string()
    .describe(
      "Primary programming language and framework, e.g. 'Go', 'TypeScript / React', 'Ballerina'",
    ),
  dependsOn: z
    .array(z.string())
    .describe(
      "Names of other components this one depends on (must match other components' 'name' values exactly)",
    ),
  entrypoint: z
    .enum([
      "deployment/service",
      "deployment/web-application",
      "cronjob/scheduled-task",
    ])
    .describe(
      "OpenChoreo component type: 'deployment/service' for backend APIs, 'deployment/web-application' for frontends/SPAs, 'cronjob/scheduled-task' for cron jobs",
    ),
  buildpack: z.literal("docker").describe("Build strategy"),
  appPath: z
    .string()
    .describe("Path within the mono-repo, e.g. '/user-api'"),
  componentAgentInstructions: z
    .string()
    .describe(
      "Detailed implementation instructions for the Generator agent",
    ),
});

export type SlimComponent = z.infer<typeof SlimComponent>;

// DesignComponent — slim + openAPISpec. This is the wire shape the BFF and
// console expect at data-finish; produced by DesignDoc.materialize().
export const DesignComponent = SlimComponent.extend({
  openAPISpec: z
    .string()
    .describe("Complete OpenAPI 3.0 YAML spec for this component"),
});

export type DesignComponent = z.infer<typeof DesignComponent>;

export const ArchitectOutput = z.object({
  overview: z
    .string()
    .describe(
      "A 2-3 sentence architecture overview summarizing the system design, component structure, and communication patterns",
    ),
  requirements: z
    .array(z.string())
    .describe(
      "Functional and non-functional requirements derived from the spec",
    ),
  components: z
    .array(DesignComponent)
    .describe("Deployable service components"),
});

export type ArchitectOutput = z.infer<typeof ArchitectOutput>;

export const ArchitectInput = z.object({
  projectName: z.string(),
  spec: z.string().describe("Specification document to design against"),
  previousDesign: ArchitectOutput.optional().describe(
    "Existing design to evolve — preserve component names and structure where possible",
  ),
});

export type ArchitectInput = z.infer<typeof ArchitectInput>;
