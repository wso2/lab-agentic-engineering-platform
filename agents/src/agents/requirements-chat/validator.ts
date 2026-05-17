import { REQUIREMENTS_MAIN_FILE, type RequirementsDoc } from "./doc.js";
import { tryDslToExcalidraw } from "../../skills/document-generation/excalidraw-dsl.js";

export interface ValidationIssue {
  filename?: string;
  message: string;
}

const MAX_FILE_BYTES = 256 * 1024;

// Mechanical bundle-level checks run at finish. The same shape is mirrored
// in the BFF after every write (defence in depth — the agent could mis-
// behave even after the validator passes).
export function validate(doc: RequirementsDoc): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const map = doc.asMap();

  if (!map[REQUIREMENTS_MAIN_FILE] || map[REQUIREMENTS_MAIN_FILE].trim().length === 0) {
    issues.push({
      filename: REQUIREMENTS_MAIN_FILE,
      message: `${REQUIREMENTS_MAIN_FILE} must exist and be non-empty.`,
    });
  }

  const seen = new Map<string, string>(); // lower-cased -> first canonical
  for (const name of Object.keys(map)) {
    const lower = name.toLowerCase();
    const prior = seen.get(lower);
    if (prior && prior !== name) {
      issues.push({
        filename: name,
        message: `Duplicate filename (case-insensitive): "${name}" vs "${prior}".`,
      });
    }
    seen.set(lower, name);

    const bytes = Buffer.byteLength(map[name]!, "utf8");
    if (bytes > MAX_FILE_BYTES) {
      issues.push({
        filename: name,
        message: `${name} is ${bytes} bytes; max is ${MAX_FILE_BYTES}.`,
      });
    }

    if (/\.dsl$/i.test(name)) {
      const kind = /^wireframes\b/i.test(name)
        ? "wireframes"
        : /^domain-model\b/i.test(name)
          ? "domain-model"
          : null;
      if (!kind) continue;
      const r = tryDslToExcalidraw(kind, map[name]!);
      if (!r.ok) {
        issues.push({
          filename: name,
          message: `${name} does not parse as a valid ${kind} DSL.`,
        });
      }
    }
  }

  return issues;
}
