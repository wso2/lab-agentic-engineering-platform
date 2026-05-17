import { lineDiff, createdDiff, deletedDiff, type DiffSummary } from "./diff.js";
import type { WireframeElement, DomainAttribute } from "./schema.js";
import {
  wireframeAddScreen,
  wireframeAddEdge,
  wireframeRemoveScreen,
  domainAddEntity,
  domainAddAttribute,
  domainAddRelation,
  domainRemoveEntity,
} from "./dsl-edit.js";

// Filename constants. The protected file is mirrored on the BFF side
// (`asdlc-service/services/artifact_store.go:33`) — keep them in sync.
export const REQUIREMENTS_MAIN_FILE = "requirements.md";
export const WIREFRAMES_DSL = "wireframes.dsl";
export const DOMAIN_DSL = "domain-model.dsl";

export interface OpResult {
  filename: string;
  newContent: string;
  diff: DiffSummary;
}

// In-memory mirror of the requirements directory for the duration of one
// chat turn. Mutated only via the tool layer; we keep all state changes
// in this one file so the validator (validator.ts) can audit the doc by
// just reading `asMap()`.
export class RequirementsDoc {
  private files: Map<string, string>;
  private touched: Set<string> = new Set();
  private scope: Set<string>;

  constructor(initialFiles: Record<string, string>) {
    this.files = new Map(Object.entries(initialFiles));
    this.scope = new Set(Object.keys(initialFiles));
  }

  read(name: string): string {
    if (!this.files.has(name)) {
      throw new Error(`File "${name}" not found.`);
    }
    return this.files.get(name)!;
  }

  has(name: string): boolean {
    return this.files.has(name);
  }

  inScope(name: string): boolean {
    return this.scope.has(name);
  }

  touchedFiles(): string[] {
    return Array.from(this.touched);
  }

  asMap(): Record<string, string> {
    return Object.fromEntries(this.files);
  }

  // -- Core ops -----------------------------------------------------------

  strReplace(
    name: string,
    oldString: string,
    newString: string,
  ): OpResult {
    if (!this.files.has(name)) {
      throw new Error(`File "${name}" not found.`);
    }
    if (/\.(dsl|excalidraw)$/i.test(name)) {
      throw new Error(
        `${name} is a canvas file — use the wireframe_/domain_ tools.`,
      );
    }
    const before = this.files.get(name)!;
    if (oldString === "") {
      throw new Error("oldString must be non-empty.");
    }
    const occurrences = countOccurrences(before, oldString);
    if (occurrences === 0) {
      throw new Error("oldString not found in file.");
    }
    if (occurrences > 1) {
      throw new Error(
        `oldString matched ${occurrences} locations — broaden it with surrounding context.`,
      );
    }
    const after = before.replace(oldString, newString);
    this.files.set(name, after);
    this.touched.add(name);
    return { filename: name, newContent: after, diff: lineDiff(before, after) };
  }

  createFile(name: string, content: string): OpResult {
    if (this.files.has(name)) {
      throw new Error(`File "${name}" already exists. Use str_replace instead.`);
    }
    if (/\.(dsl|excalidraw)$/i.test(name)) {
      throw new Error(
        "Canvas files (.dsl / .excalidraw) cannot be created via chat — use the explorer's Add document menu.",
      );
    }
    if (!/\.md$/i.test(name)) {
      throw new Error(`Filename "${name}" must end in .md.`);
    }
    this.files.set(name, content);
    this.touched.add(name);
    return { filename: name, newContent: content, diff: createdDiff(content) };
  }

  deleteFile(name: string): OpResult {
    if (!this.files.has(name)) {
      throw new Error(`File "${name}" not found.`);
    }
    if (name === REQUIREMENTS_MAIN_FILE) {
      throw new Error(`Cannot delete protected file "${REQUIREMENTS_MAIN_FILE}".`);
    }
    const content = this.files.get(name)!;
    this.files.delete(name);
    this.touched.add(name);
    return { filename: name, newContent: "", diff: deletedDiff(content) };
  }

  // -- Wireframe canvas ops ----------------------------------------------

  wireframeAddScreen(name: string, elements: WireframeElement[]): OpResult {
    return this.canvasEdit(WIREFRAMES_DSL, (dsl) =>
      wireframeAddScreen(dsl, name, elements),
    );
  }

  wireframeAddEdge(from: string, to: string): OpResult {
    return this.canvasEdit(WIREFRAMES_DSL, (dsl) =>
      wireframeAddEdge(dsl, from, to),
    );
  }

  wireframeRemoveScreen(name: string): OpResult {
    return this.canvasEdit(WIREFRAMES_DSL, (dsl) =>
      wireframeRemoveScreen(dsl, name),
    );
  }

  // -- Domain canvas ops --------------------------------------------------

  domainAddEntity(name: string, attributes: DomainAttribute[]): OpResult {
    return this.canvasEdit(DOMAIN_DSL, (dsl) =>
      domainAddEntity(dsl, name, attributes),
    );
  }

  domainAddAttribute(entity: string, attribute: DomainAttribute): OpResult {
    return this.canvasEdit(DOMAIN_DSL, (dsl) =>
      domainAddAttribute(dsl, entity, attribute),
    );
  }

  domainAddRelation(
    from: string,
    to: string,
    cardinality: string,
    label: string,
  ): OpResult {
    return this.canvasEdit(DOMAIN_DSL, (dsl) =>
      domainAddRelation(dsl, from, to, cardinality, label),
    );
  }

  domainRemoveEntity(name: string): OpResult {
    return this.canvasEdit(DOMAIN_DSL, (dsl) =>
      domainRemoveEntity(dsl, name),
    );
  }

  // -- Internal -----------------------------------------------------------

  private canvasEdit(
    filename: string,
    mutator: (dsl: string) => string,
  ): OpResult {
    if (!this.files.has(filename)) {
      throw new Error(
        `${filename} is not in the project — add a ${filename.replace(".dsl", ".excalidraw")} via the explorer first.`,
      );
    }
    const before = this.files.get(filename)!;
    const after = mutator(before);
    this.files.set(filename, after);
    this.touched.add(filename);
    return { filename, newContent: after, diff: lineDiff(before, after) };
  }
}

function countOccurrences(haystack: string, needle: string): number {
  if (needle.length === 0) return 0;
  let count = 0;
  let pos = 0;
  while (true) {
    const idx = haystack.indexOf(needle, pos);
    if (idx < 0) break;
    count++;
    pos = idx + needle.length;
  }
  return count;
}
