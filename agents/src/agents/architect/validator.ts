import { parse as parseYaml } from "yaml";
import type { DesignDoc } from "./doc.js";

// Structured validation issue. Per design doc §5.
export type ValidationIssue = {
  component?: string;
  code: string;
  // Free-form context fields keyed by issue code.
  [key: string]: unknown;
};

const ENTRYPOINT_BY_TYPE: Record<string, string> = {
  service: "deployment/service",
  "web-app": "deployment/web-application",
  "scheduled-task": "cronjob/scheduled-task",
};

const HTTP_METHODS = new Set([
  "get",
  "post",
  "put",
  "delete",
  "patch",
  "head",
  "options",
  "trace",
]);

const REF_KINDS = new Set([
  "schemas",
  "parameters",
  "requestBodies",
  "responses",
  "headers",
  "securitySchemes",
  "examples",
  "callbacks",
  "links",
]);

export function validate(doc: DesignDoc): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  validatePerComponent(doc, issues);
  validatePerOpenApi(doc, issues);
  validateCrossComponent(doc, issues);
  return issues;
}

function validatePerComponent(doc: DesignDoc, issues: ValidationIssue[]): void {
  const seenAppPaths = new Map<string, string>(); // appPath -> first component name

  for (const name of doc.pendingSpecs()) {
    issues.push({ component: name, code: "missing-spec" });
  }

  for (const [name, entry] of doc.components) {
    const slim = entry.slim;

    const expected = ENTRYPOINT_BY_TYPE[slim.componentType];
    if (expected && expected !== slim.entrypoint) {
      issues.push({
        component: name,
        code: "entrypoint-mismatch",
        componentType: slim.componentType,
        entrypoint: slim.entrypoint,
        expected,
      });
    }

    if (slim.appPath) {
      const prior = seenAppPaths.get(slim.appPath);
      if (prior !== undefined) {
        issues.push({
          component: name,
          code: "duplicate-app-path",
          appPath: slim.appPath,
          conflictsWith: prior,
        });
      } else {
        seenAppPaths.set(slim.appPath, name);
      }
    }
  }
}

function validatePerOpenApi(doc: DesignDoc, issues: ValidationIssue[]): void {
  for (const [name, entry] of doc.components) {
    if (entry.openapi === null) continue; // already flagged as missing-spec

    let parsed: unknown;
    try {
      parsed = parseYaml(entry.openapi);
    } catch (err) {
      issues.push({
        component: name,
        code: "yaml-parse-error",
        message: err instanceof Error ? err.message : String(err),
      });
      continue;
    }

    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      issues.push({ component: name, code: "openapi-not-object" });
      continue;
    }

    const spec = parsed as Record<string, unknown>;
    if (typeof spec.openapi !== "string" || spec.openapi.length === 0) {
      issues.push({ component: name, code: "openapi-version-missing" });
    }

    const slim = entry.slim;
    const paths = (spec.paths ?? {}) as Record<string, unknown>;
    const requiresPathOps =
      slim.componentType === "service" ||
      slim.componentType === "scheduled-task";
    if (requiresPathOps && Object.keys(paths).length === 0) {
      issues.push({ component: name, code: "no-path-operations" });
    }

    if (slim.componentType === "service") {
      const health = paths["/health"] as Record<string, unknown> | undefined;
      const healthOps =
        health && typeof health === "object" ? Object.keys(health) : [];
      const hasGet = healthOps.some((m) => m.toLowerCase() === "get");
      if (!hasGet) {
        issues.push({ component: name, code: "missing-health" });
      }
    }

    const operationIds = new Set<string>();
    for (const [pathKey, pathItem] of Object.entries(paths)) {
      if (!pathItem || typeof pathItem !== "object") continue;
      for (const [method, op] of Object.entries(
        pathItem as Record<string, unknown>,
      )) {
        if (method.startsWith("x-")) continue;
        if (method === "parameters" || method === "summary" || method === "description")
          continue;
        if (!HTTP_METHODS.has(method.toLowerCase())) {
          issues.push({
            component: name,
            code: "invalid-method",
            path: pathKey,
            method,
          });
          continue;
        }
        if (!op || typeof op !== "object") continue;
        const operation = op as Record<string, unknown>;

        const responses = (operation.responses ?? {}) as Record<string, unknown>;
        for (const code of Object.keys(responses)) {
          if (code === "default") continue;
          if (!/^[1-5]\d{2}$/.test(code)) {
            issues.push({
              component: name,
              code: "invalid-response-code",
              path: pathKey,
              method,
              responseCode: code,
            });
          }
        }

        if (typeof operation.operationId === "string") {
          if (operationIds.has(operation.operationId)) {
            issues.push({
              component: name,
              code: "duplicate-operation-id",
              operationId: operation.operationId,
            });
          } else {
            operationIds.add(operation.operationId);
          }
        }
      }
    }

    const components = (spec.components ?? {}) as Record<string, unknown>;
    const schemas = (components.schemas ?? {}) as Record<string, unknown>;
    for (const [schemaName, schema] of Object.entries(schemas)) {
      if (!schema || typeof schema !== "object") {
        issues.push({
          component: name,
          code: "schema-shape",
          schema: schemaName,
        });
        continue;
      }
      const s = schema as Record<string, unknown>;
      const hasShape =
        typeof s.type === "string" ||
        typeof s.$ref === "string" ||
        Array.isArray(s.allOf) ||
        Array.isArray(s.oneOf) ||
        Array.isArray(s.anyOf) ||
        (s.properties &&
          typeof s.properties === "object" &&
          Object.keys(s.properties as Record<string, unknown>).length > 0);
      if (!hasShape) {
        issues.push({
          component: name,
          code: "schema-shape",
          schema: schemaName,
        });
      }
    }

    walkRefs(spec, (ref) => {
      if (!ref.startsWith("#/components/")) {
        // Cross-doc refs aren't supported in OpenAPI; flag them.
        issues.push({ component: name, code: "unresolved-ref", ref });
        return;
      }
      const parts = ref.slice("#/components/".length).split("/");
      if (parts.length < 2) {
        issues.push({ component: name, code: "unresolved-ref", ref });
        return;
      }
      const [kind, refName] = parts;
      if (!REF_KINDS.has(kind)) {
        issues.push({ component: name, code: "unresolved-ref", ref });
        return;
      }
      const bag = (components[kind] ?? {}) as Record<string, unknown>;
      if (!Object.prototype.hasOwnProperty.call(bag, refName)) {
        issues.push({ component: name, code: "unresolved-ref", ref });
      }
    });
  }
}

function validateCrossComponent(
  doc: DesignDoc,
  issues: ValidationIssue[],
): void {
  // dependsOn names exist
  const names = new Set(doc.components.keys());
  for (const [name, entry] of doc.components) {
    for (const dep of entry.slim.dependsOn) {
      if (!names.has(dep)) {
        issues.push({ component: name, code: "dangling-dep", dep });
      }
    }
  }

  // Topological sort — detect cycles
  const graph: Record<string, string[]> = {};
  for (const [name, entry] of doc.components) {
    graph[name] = entry.slim.dependsOn.filter((d) => names.has(d));
  }
  const WHITE = 0,
    GRAY = 1,
    BLACK = 2;
  const color: Record<string, number> = {};
  for (const k of Object.keys(graph)) color[k] = WHITE;

  const cycleNodes = new Set<string>();
  function dfs(node: string, stack: string[]): void {
    color[node] = GRAY;
    stack.push(node);
    for (const next of graph[node] ?? []) {
      if (color[next] === GRAY) {
        // Cycle — record from where `next` first appears in `stack`.
        const idx = stack.indexOf(next);
        if (idx >= 0) {
          for (const n of stack.slice(idx)) cycleNodes.add(n);
        }
      } else if (color[next] === WHITE) {
        dfs(next, stack);
      }
    }
    stack.pop();
    color[node] = BLACK;
  }
  for (const node of Object.keys(graph)) {
    if (color[node] === WHITE) dfs(node, []);
  }
  for (const node of cycleNodes) {
    issues.push({ component: node, code: "depends-on-cycle" });
  }
}

// Walks every node in the parsed YAML/JSON; any object with a `$ref` string
// key is treated as a reference. This avoids enumerating every spot where a
// $ref may appear (parameters, responses, schemas, etc.).
function walkRefs(node: unknown, visit: (ref: string) => void): void {
  if (!node || typeof node !== "object") return;
  if (Array.isArray(node)) {
    for (const item of node) walkRefs(item, visit);
    return;
  }
  const obj = node as Record<string, unknown>;
  if (typeof obj.$ref === "string") {
    visit(obj.$ref);
    return;
  }
  for (const v of Object.values(obj)) walkRefs(v, visit);
}
