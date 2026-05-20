// Pure-text DSL editors for wireframes.dsl and domain-model.dsl.
//
// We deliberately do NOT round-trip through the parser+renderer for these
// edits — agents/skills/document-generation/excalidraw-dsl.ts is happy to
// drop comments / blank lines on parse, and we want the file to read like a
// human-edited document. So we operate on the raw text with light regex
// surgery, keyed off the same line tokens the grammar uses.
//
// Each helper returns the new DSL text and throws on invalid input
// (referenced screen/entity not found, name collisions, etc.). The agent's
// tool layer turns thrown errors into a `data-tool-error` frame.

import type { WireframeElement, DomainAttribute } from "./schema.js";

// -------------------------------------------------------------------------
// Common helpers
// -------------------------------------------------------------------------

function quote(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function ensureTrailingNewline(s: string): string {
  return s.endsWith("\n") ? s : s + "\n";
}

// -------------------------------------------------------------------------
// Wireframes DSL
// -------------------------------------------------------------------------

const WIREFRAME_DEFAULT_WIDTH = {
  rect: 280,
  button: 280,
  ellipse: 60,
  text: 0, // text auto-sizes; renderer ignores width/height
} as const;

const WIREFRAME_DEFAULT_HEIGHT = {
  rect: 32,
  button: 40,
  ellipse: 60,
  text: 0,
} as const;

function formatWireframeElement(el: WireframeElement): string {
  const label = quote(el.label);
  if (el.kind === "text") {
    return `  text ${label} ${el.x},${el.y}`;
  }
  const width = el.width ?? WIREFRAME_DEFAULT_WIDTH[el.kind];
  const height = el.height ?? WIREFRAME_DEFAULT_HEIGHT[el.kind];
  return `  ${el.kind} ${label} ${el.x},${el.y} ${width}x${height}`;
}

// Locate the start index of a top-level block (`screen <name>` or `flow`)
// in the DSL. Returns -1 if not found.
function findBlock(
  dsl: string,
  match: (line: string) => boolean,
): { start: number; end: number } | null {
  const lines = dsl.split("\n");
  let start = -1;
  for (let i = 0; i < lines.length; i++) {
    if (match(lines[i]!)) {
      start = i;
      break;
    }
  }
  if (start < 0) return null;
  // Block ends at the next non-indented, non-empty line (excluding the
  // start line itself).
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i]!;
    if (line.length > 0 && !/^\s/.test(line)) {
      end = i;
      break;
    }
  }
  return { start, end };
}

function listScreens(dsl: string): string[] {
  const names: string[] = [];
  for (const line of dsl.split("\n")) {
    const m = /^screen\s+([\w-]+)\s*$/i.exec(line);
    if (m) names.push(m[1]!);
  }
  return names;
}

export function wireframeAddScreen(
  dsl: string,
  name: string,
  elements: WireframeElement[],
): string {
  if (!/^[\w-]+$/.test(name)) {
    throw new Error(`Invalid screen name "${name}" — use word chars + dashes only.`);
  }
  if (listScreens(dsl).includes(name)) {
    throw new Error(`Screen "${name}" already exists. Use wireframe_remove_screen first.`);
  }

  const screenBlock = [`screen ${name}`, ...elements.map(formatWireframeElement)].join("\n");

  // Insert before `flow` if present, otherwise append at EOF.
  const flow = findBlock(dsl, (l) => /^flow\b/i.test(l));
  const lines = dsl.split("\n");
  if (flow) {
    lines.splice(flow.start, 0, screenBlock, "");
  } else {
    if (lines.length > 0 && lines[lines.length - 1] !== "") lines.push("");
    lines.push(screenBlock, "");
  }
  return ensureTrailingNewline(lines.join("\n"));
}

export function wireframeRemoveScreen(dsl: string, name: string): string {
  if (!listScreens(dsl).includes(name)) {
    throw new Error(`Screen "${name}" not found.`);
  }
  const block = findBlock(dsl, (l) => new RegExp(`^screen\\s+${name}\\s*$`, "i").test(l));
  if (!block) throw new Error(`Screen "${name}" not found.`);
  const lines = dsl.split("\n");
  lines.splice(block.start, block.end - block.start);
  // Drop trailing blank that the block may have owned.
  if (block.start < lines.length && lines[block.start] === "") {
    lines.splice(block.start, 1);
  }

  // Strip any `flow` edges referencing the removed screen.
  const out: string[] = [];
  let inFlow = false;
  for (const line of lines) {
    if (/^flow\b/i.test(line)) {
      inFlow = true;
      out.push(line);
      continue;
    }
    if (inFlow && line.length > 0 && !/^\s/.test(line)) inFlow = false;
    if (inFlow) {
      const m = /^\s+([\w-]+)\s*->\s*([\w-]+)\s*$/.exec(line);
      if (m && (m[1] === name || m[2] === name)) continue;
    }
    out.push(line);
  }
  return ensureTrailingNewline(out.join("\n"));
}

export function wireframeAddEdge(dsl: string, from: string, to: string): string {
  if (!/^[\w-]+$/.test(from) || !/^[\w-]+$/.test(to)) {
    throw new Error("Edge endpoints must be screen names (word chars + dashes).");
  }
  const screens = listScreens(dsl);
  if (!screens.includes(from)) throw new Error(`Screen "${from}" not found.`);
  if (!screens.includes(to)) throw new Error(`Screen "${to}" not found.`);

  const edge = `  ${from} -> ${to}`;
  const flow = findBlock(dsl, (l) => /^flow\b/i.test(l));
  const lines = dsl.split("\n");
  if (flow) {
    // Idempotent — skip duplicates.
    for (let i = flow.start + 1; i < flow.end; i++) {
      if (lines[i]!.trim() === `${from} -> ${to}`) {
        return ensureTrailingNewline(dsl);
      }
    }
    lines.splice(flow.end, 0, edge);
  } else {
    if (lines.length > 0 && lines[lines.length - 1] !== "") lines.push("");
    lines.push("flow", edge, "");
  }
  return ensureTrailingNewline(lines.join("\n"));
}

// -------------------------------------------------------------------------
// Domain-model DSL
// -------------------------------------------------------------------------

function listEntities(dsl: string): string[] {
  const names: string[] = [];
  for (const line of dsl.split("\n")) {
    const m = /^entity\s+([\w-]+)\s*$/i.exec(line);
    if (m) names.push(m[1]!);
  }
  return names;
}

function formatAttribute(attr: DomainAttribute): string {
  return `  ${attr.name}: ${attr.type}`;
}

export function domainAddEntity(
  dsl: string,
  name: string,
  attributes: DomainAttribute[],
): string {
  if (!/^[\w-]+$/.test(name)) {
    throw new Error(`Invalid entity name "${name}".`);
  }
  if (listEntities(dsl).includes(name)) {
    throw new Error(`Entity "${name}" already exists. Use domain_remove_entity first.`);
  }
  const block = [`entity ${name}`, ...attributes.map(formatAttribute)].join("\n");
  const lines = dsl.split("\n");

  // Insert before the first `relation` line if one exists; otherwise EOF.
  let relationStart = -1;
  for (let i = 0; i < lines.length; i++) {
    if (/^relation\b/i.test(lines[i]!)) {
      relationStart = i;
      break;
    }
  }
  if (relationStart >= 0) {
    lines.splice(relationStart, 0, block, "");
  } else {
    if (lines.length > 0 && lines[lines.length - 1] !== "") lines.push("");
    lines.push(block, "");
  }
  return ensureTrailingNewline(lines.join("\n"));
}

export function domainAddAttribute(
  dsl: string,
  entity: string,
  attr: DomainAttribute,
): string {
  if (!listEntities(dsl).includes(entity)) {
    throw new Error(`Entity "${entity}" not found.`);
  }
  const block = findBlock(dsl, (l) =>
    new RegExp(`^entity\\s+${entity}\\s*$`, "i").test(l),
  );
  if (!block) throw new Error(`Entity "${entity}" not found.`);
  const lines = dsl.split("\n");
  // Refuse duplicate attribute name.
  for (let i = block.start + 1; i < block.end; i++) {
    const m = /^\s+(\w+):/i.exec(lines[i]!);
    if (m && m[1] === attr.name) {
      throw new Error(`Attribute "${attr.name}" already exists on "${entity}".`);
    }
  }
  // Append at the end of the block (preserving a trailing blank if any).
  let insertAt = block.end;
  while (insertAt > block.start + 1 && lines[insertAt - 1] === "") insertAt--;
  lines.splice(insertAt, 0, formatAttribute(attr));
  return ensureTrailingNewline(lines.join("\n"));
}

export function domainAddRelation(
  dsl: string,
  from: string,
  to: string,
  cardinality: string,
  label: string,
): string {
  const entities = listEntities(dsl);
  if (!entities.includes(from)) throw new Error(`Entity "${from}" not found.`);
  if (!entities.includes(to)) throw new Error(`Entity "${to}" not found.`);
  const card = cardinality.trim() ? `[${cardinality.trim()}]` : "";
  const lbl = label.trim() ? ` ${quote(label.trim())}` : "";
  const line = `relation ${from} -${card}-> ${to}${lbl}`;
  const lines = dsl.split("\n");
  if (lines.length > 0 && lines[lines.length - 1] !== "") lines.push("");
  lines.push(line, "");
  return ensureTrailingNewline(lines.join("\n"));
}

export function domainRemoveEntity(dsl: string, name: string): string {
  if (!listEntities(dsl).includes(name)) {
    throw new Error(`Entity "${name}" not found.`);
  }
  const block = findBlock(dsl, (l) => new RegExp(`^entity\\s+${name}\\s*$`, "i").test(l));
  if (!block) throw new Error(`Entity "${name}" not found.`);
  const lines = dsl.split("\n");
  lines.splice(block.start, block.end - block.start);
  if (block.start < lines.length && lines[block.start] === "") {
    lines.splice(block.start, 1);
  }
  // Strip any `relation` line touching this entity.
  const re = new RegExp(`^relation\\s+(${name}\\b|.+\\b${name}\\s*("|$))`, "i");
  const filtered = lines.filter((l) => {
    if (!/^relation\b/i.test(l)) return true;
    if (re.test(l)) return false;
    // Also catch the "B" side via word-boundary scan.
    const tokens = l.split(/\s+/);
    return !tokens.includes(name);
  });
  return ensureTrailingNewline(filtered.join("\n"));
}
