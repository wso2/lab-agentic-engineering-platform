import { createAgent } from "../../shared/create-agent.js";
import { WireframeOutput } from "./schema.js";
import type { WireframeInput } from "./schema.js";
import { systemPrompt, buildUserPrompt } from "./prompt.js";

export const wireframe = createAgent<WireframeInput, WireframeOutput>({
  name: "wireframe",
  description:
    "Generates an HTML wireframe prototype from a project specification",
  systemPrompt,
  buildUserPrompt,
  outputSchema: WireframeOutput,
});

export { WireframeInput, WireframeOutput } from "./schema.js";
