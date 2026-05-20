import { test } from "node:test";
import assert from "node:assert/strict";
import { DesignDoc } from "./doc.js";
import { validate } from "./validator.js";
import type { SlimComponent } from "./schema.js";

const slim = (overrides: Partial<SlimComponent> = {}): SlimComponent => ({
  name: "todo-api",
  componentType: "service",
  language: "Go",
  dependsOn: [],
  entrypoint: "deployment/service",
  buildpack: "docker",
  appPath: "todo-api",
  componentAgentInstructions: "go service",
  ...overrides,
});

const healthYaml = `openapi: 3.0.3
info:
  title: todo-api
  version: 1.0.0
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
components:
  schemas:
    Item:
      type: object
      properties:
        id:
          type: string
`;

const noHealthYaml = `openapi: 3.0.3
info:
  title: todo-api
  version: 1.0.0
paths:
  /items:
    get:
      responses:
        "200":
          description: ok
`;

function codes(issues: ReturnType<typeof validate>): string[] {
  return issues.map((i) => i.code);
}

test("validate — clean fresh service passes", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", healthYaml);
  const issues = validate(doc);
  assert.deepEqual(issues, []);
});

test("validate — pending spec → missing-spec", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  const issues = validate(doc);
  assert.ok(codes(issues).includes("missing-spec"));
});

test("validate — service without /health → missing-health", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", noHealthYaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("missing-health"));
});

test("validate — entrypoint mismatch flagged", () => {
  const doc = new DesignDoc();
  doc.addComponent(
    slim({
      componentType: "web-app",
      entrypoint: "deployment/service", // wrong — should be web-application
    }),
  );
  doc.setOpenApi("todo-api", healthYaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("entrypoint-mismatch"));
});

test("validate — duplicate appPath flagged", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim({ name: "a", appPath: "x" }));
  doc.addComponent(slim({ name: "b", appPath: "x" }));
  doc.setOpenApi("a", healthYaml);
  doc.setOpenApi("b", healthYaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("duplicate-app-path"));
});

test("validate — appPath with leading slash flagged", () => {
  const doc = new DesignDoc();
  // The pre-fix architect emitted "/greeting-api"; the BFF's path-prefix
  // filter then silently skipped the build because GitHub push payload
  // file paths never have a leading slash. Catch it at the source.
  doc.addComponent(slim({ name: "greeting-api", appPath: "/greeting-api" }));
  doc.setOpenApi("greeting-api", healthYaml);
  const issues = validate(doc);
  assert.ok(
    codes(issues).includes("app-path-leading-slash"),
    `expected app-path-leading-slash, got: ${JSON.stringify(issues)}`,
  );
});

test("validate — appPath that looks like an http route flagged", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim({ name: "a", appPath: "../escape" }));
  doc.setOpenApi("a", healthYaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("app-path-not-relative"));
});

test("validate — dangling dependsOn flagged", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim({ dependsOn: ["nonexistent"] }));
  doc.setOpenApi("todo-api", healthYaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("dangling-dep"));
});

test("validate — dependency cycle flagged", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim({ name: "a", appPath: "a", dependsOn: ["b"] }));
  doc.addComponent(slim({ name: "b", appPath: "b", dependsOn: ["a"] }));
  doc.setOpenApi("a", healthYaml);
  doc.setOpenApi("b", healthYaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("depends-on-cycle"));
});

test("validate — yaml parse error flagged", () => {
  const doc = new DesignDoc();
  doc.addComponent(slim());
  // Unclosed flow mapping — yaml lib raises a parse error on this.
  doc.setOpenApi("todo-api", "openapi: 3.0.3\ninfo: { title: x\npaths: {}\n");
  const issues = validate(doc);
  assert.ok(
    codes(issues).includes("yaml-parse-error"),
    `expected yaml-parse-error, got: ${JSON.stringify(issues)}`,
  );
});

test("validate — invalid response code flagged", () => {
  const yaml = `openapi: 3.0.3
info: { title: x, version: "1" }
paths:
  /health:
    get:
      responses:
        ABC:
          description: bad
        "200":
          description: ok
`;
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("invalid-response-code"));
});

test("validate — unresolved $ref flagged", () => {
  const yaml = `openapi: 3.0.3
info: { title: x, version: "1" }
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Missing"
components:
  schemas:
    Item: { type: object }
`;
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yaml);
  const issues = validate(doc);
  assert.ok(codes(issues).includes("unresolved-ref"));
});

test("validate — schema with implicit object (properties only) passes", () => {
  const yaml = `openapi: 3.0.3
info: { title: x, version: "1" }
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
components:
  schemas:
    Item:
      properties:
        id:
          type: string
`;
  const doc = new DesignDoc();
  doc.addComponent(slim());
  doc.setOpenApi("todo-api", yaml);
  const issues = validate(doc);
  assert.equal(
    codes(issues).filter((c) => c === "schema-shape").length,
    0,
  );
});
