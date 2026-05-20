import { test } from "node:test";
import assert from "node:assert/strict";
import {
  dslToExcalidraw,
  layoutDomainModel,
  tryDslToExcalidraw,
} from "./excalidraw-dsl.js";

test("wireframes DSL → numbered screens with per-flow →(N) markers, no arrows", () => {
  const dsl = `
screen Login
  text "Sign in" 20,8
  rect "Email" 20,40 280x32
  rect "Password" 20,84 280x32
  button "Submit" 20,140 280x40

screen Home
  text "Welcome" 20,8
  rect "Content" 20,40 320x400

flow
  Login -> Home
`;
  const json = dslToExcalidraw("wireframes", dsl);
  const scene = JSON.parse(json) as {
    type: string;
    elements: { type: string; text?: string }[];
  };
  assert.equal(scene.type, "excalidraw");
  // No straight flow arrows — flows render as text markers, not lines.
  assert.ok(!scene.elements.some((e) => e.type === "arrow"));
  const texts = scene.elements
    .filter((e) => e.type === "text")
    .map((e) => e.text);
  // Screens, elements, and number badges all surface as text.
  assert.ok(texts.includes("Login"));
  assert.ok(texts.includes("Home"));
  assert.ok(texts.includes("Email"));
  // Each screen carries its (N) badge.
  assert.ok(texts.includes("(1)"));
  assert.ok(texts.includes("(2)"));
  // The flow Login -> Home renders as `→(2)` next to Login's last button.
  assert.ok(texts.some((t) => t === "→(2)"));
});

test("domain-model DSL → entities laid out into layers with straight arrows", () => {
  const dsl = `
entity Project
  id: uuid
  name: string

entity Component
  id: uuid
  name: string

relation Project -[1..*]-> Component "has"
`;
  const json = dslToExcalidraw("domain-model", dsl);
  const scene = JSON.parse(json) as {
    elements: { type: string; text?: string; elbowed?: boolean; y?: number }[];
  };
  assert.ok(
    scene.elements.length >= 8,
    `expected at least 8 elements, got ${scene.elements.length}`,
  );
  const arrows = scene.elements.filter((e) => e.type === "arrow");
  assert.equal(arrows.length, 1);
  // Straight arrows — Excalidraw's elbow router was crossing other
  // entities, so the renderer emits direct closest-edge connectors instead.
  assert.equal(arrows[0]!.elbowed, false);
  const texts = scene.elements
    .filter((e) => e.type === "text")
    .map((e) => e.text);
  assert.ok(texts.includes("Project"));
  assert.ok(texts.includes("Component"));
  assert.ok(texts.some((t) => t?.startsWith("name:")));
});

test("layoutDomainModel places parent above child for a directed relation", () => {
  const layout = layoutDomainModel({
    entities: [
      { name: "Project", attrs: [{ name: "id", type: "uuid" }] },
      { name: "Component", attrs: [{ name: "id", type: "uuid" }] },
    ],
    relations: [
      { from: "Project", to: "Component", cardinality: "1..*", label: "has" },
    ],
  });
  const project = layout.nodes.get("project");
  const component = layout.nodes.get("component");
  assert.ok(project);
  assert.ok(component);
  assert.equal(project!.layer, 0);
  assert.equal(component!.layer, 1);
  assert.ok(component!.y > project!.y);
  assert.equal(layout.edges.length, 1);
  assert.equal(layout.edges[0]!.kind, "forward");
});

test("domain-model arrows are bound to both source and target entities", () => {
  const dsl = `
entity A
  id: uuid

entity B
  id: uuid

entity C
  id: uuid

relation A -[1..1]-> B "owns"
relation A -[1..1]-> C "owns"
`;
  const json = dslToExcalidraw("domain-model", dsl);
  const scene = JSON.parse(json) as {
    elements: {
      type: string;
      startBinding?: { elementId?: string };
      endBinding?: { elementId?: string };
    }[];
  };
  const arrows = scene.elements.filter((e) => e.type === "arrow");
  assert.equal(arrows.length, 2);
  for (const a of arrows) {
    assert.ok(
      a.startBinding?.elementId,
      "startBinding.elementId required so the arrow follows its source",
    );
    assert.ok(
      a.endBinding?.elementId,
      "endBinding.elementId required so the arrow follows its target",
    );
  }
});

test("layoutDomainModel flags cycle relations as back edges", () => {
  const layout = layoutDomainModel({
    entities: [
      { name: "A", attrs: [] },
      { name: "B", attrs: [] },
    ],
    relations: [
      { from: "A", to: "B", cardinality: "", label: "" },
      { from: "B", to: "A", cardinality: "", label: "" },
    ],
  });
  const kinds = layout.edges.map((e) => e.kind).sort();
  assert.deepEqual(kinds, ["back", "forward"]);
});

test("tryDslToExcalidraw returns ok=false for empty/unparseable input", () => {
  assert.deepEqual(tryDslToExcalidraw("wireframes", ""), { ok: false });
  assert.deepEqual(tryDslToExcalidraw("wireframes", "   \n  "), { ok: false });
  // Header-only — no screens declared yet, so no elements emitted.
  assert.deepEqual(tryDslToExcalidraw("wireframes", "// just a comment"), {
    ok: false,
  });
});

test("tryDslToExcalidraw renders progressive partial DSL once a screen exists", () => {
  // Just enough to produce a screen rectangle.
  const partial = "screen Login\n  rect \"Email\" 20,40 280x32";
  const result = tryDslToExcalidraw("wireframes", partial);
  assert.equal(result.ok, true);
  if (result.ok) {
    const scene = JSON.parse(result.json) as { elements: unknown[] };
    assert.ok(scene.elements.length >= 3);
  }
});
