import { parse as parseYaml } from "yaml";
import type {
  ArchitectOutput,
  DependentApi,
  DesignComponent,
  SlimComponent,
} from "./schema.js";

export type ComponentEntry = {
  slim: SlimComponent;
  // Raw YAML, opaque to DesignDoc. null = pending (no spec yet).
  openapi: string | null;
};

export type SetOpenApiResult =
  | { changed: true }
  | { changed: false; reason: "semantic_equal_to_current" };

// Per-request, in-memory representation of a design under construction.
// The architect agent mutates this via tool calls; materialize() emits the
// canonical ArchitectOutput shipped over the wire.
export class DesignDoc {
  overview: string = "";
  // Iteration order is insertion order (Map preserves it).
  components: Map<string, ComponentEntry> = new Map();

  static fromPrevious(prev?: ArchitectOutput): DesignDoc {
    const doc = new DesignDoc();
    if (!prev) return doc;
    doc.overview = prev.overview;
    for (const c of prev.components) {
      const { openAPISpec, ...slim } = c;
      doc.components.set(c.name, {
        slim,
        openapi: openAPISpec ?? null,
      });
    }
    return doc;
  }

  // ── Top-level mutators ────────────────────────────────────────────────

  setOverview(text: string): void {
    this.overview = text;
  }

  // ── Component mutators ────────────────────────────────────────────────

  addComponent(slim: SlimComponent): void {
    if (this.components.has(slim.name)) {
      throw new Error(`component ${slim.name} already exists`);
    }
    this.components.set(slim.name, { slim: { ...slim }, openapi: null });
  }

  removeComponent(name: string): void {
    if (!this.components.has(name)) {
      throw new Error(`component ${name} does not exist`);
    }
    this.components.delete(name);
  }

  // Replaces componentAgentInstructions. Does NOT invalidate openapi.
  setAgentInstructions(name: string, text: string): void {
    const entry = this.requireEntry(name);
    entry.slim = { ...entry.slim, componentAgentInstructions: text };
  }

  // Invalidates openapi — language change implies port/framework drift.
  setLanguage(name: string, language: string): void {
    const entry = this.requireEntry(name);
    entry.slim = { ...entry.slim, language };
    entry.openapi = null;
  }

  // Union-add. Invalidates openapi (caller wires a new contract).
  addDependency(name: string, dependsOn: string): void {
    const entry = this.requireEntry(name);
    if (entry.slim.dependsOn.includes(dependsOn)) {
      // Idempotent: already present, still invalidate caller intent.
      return;
    }
    entry.slim = {
      ...entry.slim,
      dependsOn: [...entry.slim.dependsOn, dependsOn],
    };
    entry.openapi = null;
  }

  // Invalidates openapi.
  removeDependency(name: string, dependsOn: string): void {
    const entry = this.requireEntry(name);
    if (!entry.slim.dependsOn.includes(dependsOn)) return;
    entry.slim = {
      ...entry.slim,
      dependsOn: entry.slim.dependsOn.filter((d) => d !== dependsOn),
    };
    entry.openapi = null;
  }

  // Adds (or replaces by name) an external dependent API. Idempotent on name.
  // Does NOT invalidate openapi — external calls are described in
  // componentAgentInstructions, not the wire contract.
  addDependentApi(name: string, dep: DependentApi): void {
    const entry = this.requireEntry(name);
    const existing = entry.slim.dependentApis ?? [];
    const filtered = existing.filter((d) => d.name !== dep.name);
    entry.slim = { ...entry.slim, dependentApis: [...filtered, dep] };
  }

  removeDependentApi(name: string, depName: string): void {
    const entry = this.requireEntry(name);
    const existing = entry.slim.dependentApis ?? [];
    if (!existing.some((d) => d.name === depName)) return;
    entry.slim = {
      ...entry.slim,
      dependentApis: existing.filter((d) => d.name !== depName),
    };
  }

  setDbEngine(name: string, engine: "mysql" | "mongodb"): void {
    const entry = this.requireEntry(name);
    entry.slim = { ...entry.slim, dbEngine: engine };
  }

  // ── OpenAPI ───────────────────────────────────────────────────────────

  // Sets the spec. Returns `changed:false` if the new spec is semantically
  // equal to what we already have (idempotency). Used to suppress no-op SSE
  // events and prevent the model from chasing its own tail.
  setOpenApi(name: string, yaml: string): SetOpenApiResult {
    const entry = this.requireEntry(name);
    if (entry.openapi !== null) {
      try {
        const oldDoc = parseYaml(entry.openapi);
        const newDoc = parseYaml(yaml);
        if (deepEqual(oldDoc, newDoc)) {
          return { changed: false, reason: "semantic_equal_to_current" };
        }
      } catch {
        // Parse failure on either side — fall through and write. The
        // validator (called from finalize) will surface parse errors.
      }
    }
    entry.openapi = yaml;
    return { changed: true };
  }

  // ── Reads ─────────────────────────────────────────────────────────────

  getComponent(name: string): { slim: SlimComponent; openapi: string | null } {
    const entry = this.requireEntry(name);
    return { slim: entry.slim, openapi: entry.openapi };
  }

  hasComponent(name: string): boolean {
    return this.components.has(name);
  }

  pendingSpecs(): string[] {
    const pending: string[] = [];
    for (const [name, entry] of this.components) {
      // web-app components do not publish a wire contract; the architect
      // prompt forbids set_openapi for them, so they are never "pending".
      if (entry.slim.componentType === "web-app") continue;
      // Database components never need an OpenAPI spec.
      if (entry.slim.componentType === "database") continue;
      if (entry.openapi === null) pending.push(name);
    }
    return pending;
  }

  // ── Output ────────────────────────────────────────────────────────────

  // Produces the canonical ArchitectOutput shipped at data-finish. Pending
  // components emit empty-string openAPISpec; finalize() should refuse to
  // emit a finish if any are pending.
  materialize(): ArchitectOutput {
    const components: DesignComponent[] = [];
    for (const [, entry] of this.components) {
      components.push({
        ...entry.slim,
        openAPISpec: entry.slim.componentType === "database" ? "" : (entry.openapi ?? ""),
      });
    }
    return {
      overview: this.overview,
      components,
    };
  }

  // ── Internals ─────────────────────────────────────────────────────────

  private requireEntry(name: string): ComponentEntry {
    const entry = this.components.get(name);
    if (!entry) throw new Error(`component ${name} does not exist`);
    return entry;
  }
}

// Structural deep-equality. Used only for OpenAPI semantic-equality check;
// we expect plain objects/arrays/scalars produced by yaml.parse.
function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a === null || b === null) return a === b;
  if (typeof a !== typeof b) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) {
      if (!deepEqual(a[i], b[i])) return false;
    }
    return true;
  }
  if (typeof a === "object") {
    if (typeof b !== "object" || Array.isArray(b)) return false;
    const ao = a as Record<string, unknown>;
    const bo = b as Record<string, unknown>;
    const aKeys = Object.keys(ao);
    const bKeys = Object.keys(bo);
    if (aKeys.length !== bKeys.length) return false;
    for (const k of aKeys) {
      if (!Object.prototype.hasOwnProperty.call(bo, k)) return false;
      if (!deepEqual(ao[k], bo[k])) return false;
    }
    return true;
  }
  return false;
}
