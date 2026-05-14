import { test } from "node:test";
import assert from "node:assert/strict";
import { dslToExcalidraw, tryDslToExcalidraw } from "./excalidraw-dsl.js";

test("wireframes DSL → Excalidraw scene with screens, elements, flow arrows", () => {
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
  // 2 screens (each: outer rect + header text) + 4 elements on Login (rect+label, rect+label, rect+label, button+label = 8 elements) + 2 elements on Home (text=1, rect+label=2) + 1 flow arrow
  assert.ok(
    scene.elements.length >= 12,
    `expected at least 12 elements, got ${scene.elements.length}`,
  );
  // Has at least one arrow (the flow)
  assert.ok(scene.elements.some((e) => e.type === "arrow"));
  // Has the screen names as text labels
  const texts = scene.elements
    .filter((e) => e.type === "text")
    .map((e) => e.text);
  assert.ok(texts.includes("Login"));
  assert.ok(texts.includes("Home"));
  assert.ok(texts.includes("Email"));
});

test("domain-model DSL → Excalidraw scene with entities, attributes, relations", () => {
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
    elements: { type: string; text?: string }[];
  };
  // 2 entities (each: outer rect + name text + 2 attr texts = 4 elements) + 1 relation arrow (+optional label)
  assert.ok(
    scene.elements.length >= 8,
    `expected at least 8 elements, got ${scene.elements.length}`,
  );
  assert.ok(scene.elements.some((e) => e.type === "arrow"));
  const texts = scene.elements
    .filter((e) => e.type === "text")
    .map((e) => e.text);
  assert.ok(texts.includes("Project"));
  assert.ok(texts.includes("Component"));
  assert.ok(texts.some((t) => t?.startsWith("name:")));
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
