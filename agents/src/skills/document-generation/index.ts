import type { DocumentGenerationSkill } from "./types.js";
import { requirementsFromPrompt } from "./requirements-from-prompt.js";
import { functionalRequirements } from "./functional-requirements.js";
import { nonFunctionalRequirements } from "./non-functional-requirements.js";
import { userStories } from "./user-stories.js";
import { wireframes } from "./wireframes.js";
import { domainModel } from "./domain-model.js";
import { componentDesign } from "./component-design.js";
import { componentOpenApi } from "./component-openapi.js";

const SKILLS: DocumentGenerationSkill[] = [
  requirementsFromPrompt,
  functionalRequirements,
  nonFunctionalRequirements,
  userStories,
  wireframes,
  domainModel,
  componentDesign,
  componentOpenApi,
];

const SKILLS_BY_ID = new Map<string, DocumentGenerationSkill>(
  SKILLS.map((s) => [s.id, s]),
);

export function getDocumentGenerationSkill(
  id: string,
): DocumentGenerationSkill | undefined {
  return SKILLS_BY_ID.get(id);
}

export function listDocumentGenerationSkills(): DocumentGenerationSkill[] {
  return [...SKILLS];
}

export type { DocumentGenerationSkill, SkillInput } from "./types.js";
