import { test } from "node:test";
import assert from "node:assert/strict";
import { DesignDoc } from "./doc.js";
import type { ArchitectOutput, SlimComponent } from "./schema.js";

const slim = (overrides: Partial<SlimComponent> = {}): SlimComponent => ({
  name: "todo-api",
  componentType: "service",
  language: "Go",
  dependsOn: [],
  entrypoint: "deployment/service",
  buildpack: "docker",
  appPath: "todo-api",
  componentAgentInstructions: "implement a Go service",
  ...overrides,
});

const yamlA = `openapi: 3.0.3
info:
  title: todo-api
  version: 1.0.0
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
`;

const yamlAReordered = `openapi: 3.0.3
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
info:
  version: 1.0.0
  title: todo-api
`;

const yamlB = `openapi: 3.0.3
info:
  title: todo-api
  version: 2.0.0
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
`;

test("DesignDoc.fromPrevious populates components with openapi", () => {
  const prev: ArchitectOutput = {
    overview: "x",
    components: [{ ...slim(), openAPISpec: yamlA }],
  };
  const doc = DesignDoc.fromPrevious(prev);
  assert.equal(doc.overview, "x");
  const entry = doc.getComponent("todo-api");
  assert.equal(entry.openapi, yamlA);
  assert.deepEqual(doc.pendingSpecs(), []);
});

test("addComponent — duplicate name throws", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  assert.throws(() => doc.addComponent(slim()));
});

test("addComponent — fresh component has openapi=null and is pending", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  assert.deepEqual(doc.pendingSpecs(), ["todo-api"]);
  assert.equal(doc.getComponent("todo-api").openapi, null);
});

test("pendingSpecs — web-app components are excluded (no wire contract)", () => {
  const doc = new DesignDoc();
  doc.addComponent(
    slim({
      name: "ui",
      componentType: "web-app",
      entrypoint: "deployment/web-application",
      appPath: "ui",
    }),
  );
  doc.addComponent(slim({ name: "api", appPath: "api" }));
  // ui has no openapi but should NOT be pending; api should.
  assert.deepEqual(doc.pendingSpecs(), ["api"]);
});

test("setOpenApi — first set returns changed:true", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  const r = doc.setOpenApi("todo-api", yamlA);
  assert.deepEqual(r, { changed: true });
  assert.deepEqual(doc.pendingSpecs(), []);
});

test("setOpenApi — re-setting equal YAML (different key order) returns changed:false", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yamlA);
  const r = doc.setOpenApi("todo-api", yamlAReordered);
  assert.deepEqual(r, {
    changed: false,
    reason: "semantic_equal_to_current",
  });
});

test("setOpenApi — different content returns changed:true", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yamlA);
  const r = doc.setOpenApi("todo-api", yamlB);
  assert.deepEqual(r, { changed: true });
});

test("setLanguage invalidates openapi", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yamlA);
  doc.setLanguage("todo-api", "Ballerina");
  assert.equal(doc.getComponent("todo-api").openapi, null);
});

test("addDependency invalidates openapi; idempotent", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.addComponent(slim({ name: "notif" }));
  doc.setOpenApi("todo-api", yamlA);
  doc.addDependency("todo-api", "notif");
  assert.equal(doc.getComponent("todo-api").openapi, null);
  assert.deepEqual(doc.getComponent("todo-api").slim.dependsOn, ["notif"]);
  // Re-adding doesn't grow the array.
  doc.setOpenApi("todo-api", yamlA);
  doc.addDependency("todo-api", "notif");
  assert.deepEqual(doc.getComponent("todo-api").slim.dependsOn, ["notif"]);
});

test("setAgentInstructions does NOT invalidate openapi", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yamlA);
  doc.setAgentInstructions("todo-api", "new instructions");
  assert.equal(doc.getComponent("todo-api").openapi, yamlA);
});

test("removeComponent clears the entry", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.removeComponent("todo-api");
  assert.equal(doc.hasComponent("todo-api"), false);
  assert.throws(() => doc.removeComponent("todo-api"));
});

test("materialize round-trip stable", () => {
  const prev: ArchitectOutput = {
    overview: "x",
    components: [
      { ...slim({ name: "a" }), openAPISpec: yamlA },
      { ...slim({ name: "b" }), openAPISpec: yamlB },
    ],
  };
  const doc = DesignDoc.fromPrevious(prev);
  const out = doc.materialize();
  assert.equal(out.overview, "x");
  assert.equal(out.components.length, 2);
  assert.equal(out.components[0]!.name, "a");
  assert.equal(out.components[0]!.openAPISpec, yamlA);
});

test("materialize emits empty string for pending openapi", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  const out = doc.materialize();
  assert.equal(out.components[0]!.openAPISpec, "");
});
