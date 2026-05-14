// Tiny DSL → Excalidraw scene converter, used by:
//   • agents-service skills (server-side, applied at stream finish)
//   • console (best-effort live preview while the DSL streams in)
//
// The agents/ project keeps its own copy at
// `agents/src/skills/document-generation/excalidraw-dsl.ts` because it lives
// in a separate Docker context and is not part of the pnpm workspace. KEEP
// THE TWO FILES IN SYNC — fixtures in `__fixtures__/` are the canonical
// reference both copies must match.

export type DslKind = 'wireframes' | 'domain-model';

// ---------- Wireframes DSL ----------

interface WireframeElement {
  kind: 'rect' | 'ellipse' | 'button' | 'text';
  label: string;
  x: number;
  y: number;
  width: number;
  height: number;
}

interface WireframeScreen {
  name: string;
  elements: WireframeElement[];
}

interface WireframeFlow {
  from: string;
  to: string;
}

interface WireframeAst {
  screens: WireframeScreen[];
  flows: WireframeFlow[];
}

// ---------- Domain Model DSL ----------

interface DomainAttribute {
  name: string;
  type: string;
}

interface DomainEntity {
  name: string;
  attrs: DomainAttribute[];
}

interface DomainRelation {
  from: string;
  to: string;
  cardinality: string;
  label: string;
}

interface DomainAst {
  entities: DomainEntity[];
  relations: DomainRelation[];
}

// ---------- Excalidraw scene shape ----------

export interface ExcalidrawScene {
  type: 'excalidraw';
  version: number;
  source: string;
  elements: ExcalidrawElement[];
  appState: { viewBackgroundColor: string };
  files: Record<string, never>;
}

interface ExcalidrawElementBase {
  id: string;
  type: string;
  x: number;
  y: number;
  width: number;
  height: number;
  angle: number;
  strokeColor: string;
  backgroundColor: string;
  fillStyle: string;
  strokeWidth: number;
  strokeStyle: string;
  roughness: number;
  opacity: number;
  groupIds: string[];
  frameId: null;
  roundness: { type: number } | null;
  seed: number;
  versionNonce: number;
  version: number;
  isDeleted: boolean;
  boundElements: { id: string; type: string }[] | null;
  updated: number;
  link: null;
  locked: boolean;
}

interface RectElement extends ExcalidrawElementBase {
  type: 'rectangle' | 'ellipse' | 'diamond';
}

interface TextElement extends ExcalidrawElementBase {
  type: 'text';
  text: string;
  fontSize: number;
  fontFamily: number;
  textAlign: 'left' | 'center' | 'right';
  verticalAlign: 'top' | 'middle' | 'bottom';
  baseline: number;
  containerId: string | null;
  originalText: string;
  lineHeight: number;
  autoResize: boolean;
}

interface ArrowElement extends ExcalidrawElementBase {
  type: 'arrow' | 'line';
  points: [number, number][];
  lastCommittedPoint: null;
  startBinding: { elementId: string; focus: number; gap: number } | null;
  endBinding: { elementId: string; focus: number; gap: number } | null;
  startArrowhead: null | 'arrow';
  endArrowhead: null | 'arrow';
  elbowed?: boolean;
}

type ExcalidrawElement = RectElement | TextElement | ArrowElement;

// ---------- Public API ----------

export function dslToExcalidraw(kind: DslKind, dsl: string): string {
  const elements =
    kind === 'wireframes'
      ? renderWireframes(parseWireframesDsl(dsl))
      : renderDomainModel(parseDomainModelDsl(dsl));
  const scene: ExcalidrawScene = {
    type: 'excalidraw',
    version: 2,
    source: 'asdlc-generator',
    elements,
    appState: { viewBackgroundColor: '#ffffff' },
    files: {},
  };
  return JSON.stringify(scene, null, 2);
}

export function tryDslToExcalidraw(
  kind: DslKind,
  dsl: string,
): { ok: true; json: string } | { ok: false } {
  if (!dsl || dsl.trim().length === 0) return { ok: false };
  try {
    const json = dslToExcalidraw(kind, dsl);
    const parsed = JSON.parse(json) as ExcalidrawScene;
    if (parsed.elements.length === 0) return { ok: false };
    return { ok: true, json };
  } catch {
    return { ok: false };
  }
}

// ---------- Wireframes parser ----------

const QUOTED = /"((?:[^"\\]|\\.)*)"/;
const COORDS = /(\d+)\s*,\s*(\d+)/;
const SIZE = /(\d+)\s*x\s*(\d+)/;

function parseWireframesDsl(dsl: string): WireframeAst {
  const ast: WireframeAst = { screens: [], flows: [] };
  let currentScreen: WireframeScreen | null = null;
  let inFlow = false;

  for (const rawLine of dsl.split(/\r?\n/)) {
    const line = rawLine.replace(/\s+$/, '');
    if (line.trim().length === 0) continue;
    if (line.trim().startsWith('//') || line.trim().startsWith('#')) continue;

    const indented = /^\s+/.test(line);
    const trimmed = line.trim();

    if (!indented) {
      // Top-level: screen / flow
      const screenMatch = /^screen\s+(.+)$/i.exec(trimmed);
      if (screenMatch) {
        currentScreen = { name: screenMatch[1]!.trim(), elements: [] };
        ast.screens.push(currentScreen);
        inFlow = false;
        continue;
      }
      if (/^flow\b/i.test(trimmed)) {
        currentScreen = null;
        inFlow = true;
        continue;
      }
      // Unknown top-level — bail out of any nesting.
      currentScreen = null;
      inFlow = false;
      continue;
    }

    if (inFlow) {
      const flowMatch = /^([\w-]+)\s*->\s*([\w-]+)$/.exec(trimmed);
      if (flowMatch) {
        ast.flows.push({ from: flowMatch[1]!, to: flowMatch[2]! });
      }
      continue;
    }

    if (currentScreen) {
      const el = parseWireframeElement(trimmed);
      if (el) currentScreen.elements.push(el);
    }
  }

  return ast;
}

function parseWireframeElement(line: string): WireframeElement | null {
  const kindMatch = /^(rect|ellipse|button|text)\b/i.exec(line);
  if (!kindMatch) return null;
  const kind = kindMatch[1]!.toLowerCase() as WireframeElement['kind'];
  const rest = line.slice(kindMatch[0].length).trim();

  const labelMatch = QUOTED.exec(rest);
  const label = labelMatch ? unescapeQuoted(labelMatch[1]!) : '';
  const afterLabel = labelMatch ? rest.slice(labelMatch.index + labelMatch[0].length).trim() : rest;

  const coordsMatch = COORDS.exec(afterLabel);
  if (!coordsMatch) return null;
  const x = parseInt(coordsMatch[1]!, 10);
  const y = parseInt(coordsMatch[2]!, 10);

  let width = kind === 'text' ? Math.max(60, label.length * 9) : 160;
  let height = kind === 'text' ? 24 : 32;
  const sizeMatch = SIZE.exec(afterLabel.slice(coordsMatch.index + coordsMatch[0].length));
  if (sizeMatch) {
    width = parseInt(sizeMatch[1]!, 10);
    height = parseInt(sizeMatch[2]!, 10);
  }

  return { kind, label, x, y, width, height };
}

function unescapeQuoted(s: string): string {
  return s.replace(/\\"/g, '"').replace(/\\\\/g, '\\');
}

// ---------- Wireframes renderer ----------

const SCREEN_W = 360;
const SCREEN_H = 540;
const SCREEN_GAP_X = 80;
const SCREEN_GAP_Y = 80;
const SCREEN_HEADER_H = 36;
const COLUMNS = 3;

function renderWireframes(ast: WireframeAst): ExcalidrawElement[] {
  const out: ExcalidrawElement[] = [];
  // Map screen-name → bounding box for flow arrow binding.
  const screenBoxes = new Map<string, { id: string; x: number; y: number; w: number; h: number }>();

  ast.screens.forEach((screen, idx) => {
    const col = idx % COLUMNS;
    const row = Math.floor(idx / COLUMNS);
    const sx = col * (SCREEN_W + SCREEN_GAP_X);
    const sy = row * (SCREEN_H + SCREEN_GAP_Y);
    const screenId = stableId(`screen:${screen.name}:${idx}`);

    out.push(makeRect(screenId, sx, sy, SCREEN_W, SCREEN_H, '#1e1e1e', 'transparent'));
    out.push(
      makeText(
        stableId(`screen-label:${screen.name}:${idx}`),
        sx + 12,
        sy + 8,
        SCREEN_W - 24,
        SCREEN_HEADER_H - 12,
        screen.name,
        16,
        'left',
      ),
    );
    // Header divider line is omitted to keep elements light; the screen name
    // sits inside the rectangle near the top, which reads as a header.

    screenBoxes.set(screen.name.toLowerCase(), {
      id: screenId,
      x: sx,
      y: sy,
      w: SCREEN_W,
      h: SCREEN_H,
    });

    for (const el of screen.elements) {
      const ex = sx + 12 + el.x; // 12px inner padding
      const ey = sy + SCREEN_HEADER_H + el.y;
      const eid = stableId(`el:${screen.name}:${el.kind}:${el.label}:${ex}:${ey}`);
      switch (el.kind) {
        case 'rect':
          out.push(makeRect(eid, ex, ey, el.width, el.height, '#1971c2', '#a5d8ff'));
          out.push(
            makeText(
              stableId(`${eid}:label`),
              ex + 6,
              ey + 6,
              el.width - 12,
              el.height - 12,
              el.label,
              14,
              'left',
            ),
          );
          break;
        case 'button':
          out.push(makeRect(eid, ex, ey, el.width, el.height, '#2f9e44', '#b2f2bb', { type: 3 }));
          out.push(
            makeText(
              stableId(`${eid}:label`),
              ex,
              ey + Math.max(0, (el.height - 14) / 2),
              el.width,
              14,
              el.label,
              14,
              'center',
            ),
          );
          break;
        case 'ellipse':
          out.push({
            ...makeRect(eid, ex, ey, el.width, el.height, '#1e1e1e', '#ffd8a8'),
            type: 'ellipse',
          });
          out.push(
            makeText(
              stableId(`${eid}:label`),
              ex,
              ey + Math.max(0, (el.height - 14) / 2),
              el.width,
              14,
              el.label,
              14,
              'center',
            ),
          );
          break;
        case 'text':
          out.push(
            makeText(eid, ex, ey, el.width, el.height, el.label, 14, 'left'),
          );
          break;
      }
    }
  });

  // Flow arrows: connect screen boxes by name (case-insensitive).
  for (const [i, flow] of ast.flows.entries()) {
    const a = screenBoxes.get(flow.from.toLowerCase());
    const b = screenBoxes.get(flow.to.toLowerCase());
    if (!a || !b) continue;
    const startX = a.x + a.w;
    const startY = a.y + a.h / 2;
    const endX = b.x;
    const endY = b.y + b.h / 2;
    out.push(
      makeArrow(
        stableId(`flow:${flow.from}->${flow.to}:${i}`),
        startX,
        startY,
        endX,
        endY,
        a.id,
        b.id,
      ),
    );
  }

  return out;
}

// ---------- Domain Model parser ----------

function parseDomainModelDsl(dsl: string): DomainAst {
  const ast: DomainAst = { entities: [], relations: [] };
  let currentEntity: DomainEntity | null = null;

  for (const rawLine of dsl.split(/\r?\n/)) {
    const line = rawLine.replace(/\s+$/, '');
    if (line.trim().length === 0) continue;
    if (line.trim().startsWith('//') || line.trim().startsWith('#')) continue;

    const indented = /^\s+/.test(line);
    const trimmed = line.trim();

    if (!indented) {
      const entityMatch = /^entity\s+([\w-]+)\b/i.exec(trimmed);
      if (entityMatch) {
        currentEntity = { name: entityMatch[1]!, attrs: [] };
        ast.entities.push(currentEntity);
        continue;
      }
      const relMatch =
        /^relation\s+([\w-]+)\s*-\s*(?:\[([^\]]*)\])?\s*->\s*([\w-]+)(?:\s+"([^"]*)")?/i.exec(
          trimmed,
        );
      if (relMatch) {
        ast.relations.push({
          from: relMatch[1]!,
          to: relMatch[3]!,
          cardinality: (relMatch[2] ?? '').trim(),
          label: relMatch[4] ?? '',
        });
        currentEntity = null;
        continue;
      }
      const undirectedMatch = /^relation\s+([\w-]+)\s*--\s*([\w-]+)(?:\s+"([^"]*)")?/i.exec(trimmed);
      if (undirectedMatch) {
        ast.relations.push({
          from: undirectedMatch[1]!,
          to: undirectedMatch[2]!,
          cardinality: '',
          label: undirectedMatch[3] ?? '',
        });
        currentEntity = null;
        continue;
      }
      currentEntity = null;
      continue;
    }

    if (currentEntity) {
      const attrMatch = /^([\w-]+)\s*:\s*(.+)$/.exec(trimmed);
      if (attrMatch) {
        currentEntity.attrs.push({ name: attrMatch[1]!, type: attrMatch[2]!.trim() });
      }
    }
  }

  return ast;
}

// ---------- Domain Model renderer ----------

const ENTITY_W = 220;
const ENTITY_HEADER_H = 32;
const ATTR_LINE_H = 22;
const ENTITY_GAP_X = 100;
const ENTITY_GAP_Y = 80;
const ENTITY_COLS = 3;

function renderDomainModel(ast: DomainAst): ExcalidrawElement[] {
  const out: ExcalidrawElement[] = [];
  const entityBoxes = new Map<
    string,
    { id: string; x: number; y: number; w: number; h: number }
  >();

  ast.entities.forEach((entity, idx) => {
    const col = idx % ENTITY_COLS;
    const row = Math.floor(idx / ENTITY_COLS);
    const x = col * (ENTITY_W + ENTITY_GAP_X);
    // Row height varies: track max-height per row by computing each entity's height first.
    const rowHeights = computeRowHeights(ast.entities);
    const y = sumRowHeights(rowHeights, row, ENTITY_GAP_Y);

    const h = ENTITY_HEADER_H + Math.max(1, entity.attrs.length) * ATTR_LINE_H + 12;
    const id = stableId(`entity:${entity.name}:${idx}`);
    out.push(makeRect(id, x, y, ENTITY_W, h, '#1e1e1e', '#fff9db'));
    // Header
    out.push(
      makeText(
        stableId(`entity-name:${entity.name}:${idx}`),
        x + 12,
        y + 8,
        ENTITY_W - 24,
        20,
        entity.name,
        16,
        'left',
      ),
    );
    // Attributes
    entity.attrs.forEach((attr, ai) => {
      out.push(
        makeText(
          stableId(`attr:${entity.name}:${attr.name}:${ai}`),
          x + 12,
          y + ENTITY_HEADER_H + ai * ATTR_LINE_H + 4,
          ENTITY_W - 24,
          ATTR_LINE_H - 4,
          `${attr.name}: ${attr.type}`,
          13,
          'left',
        ),
      );
    });
    entityBoxes.set(entity.name.toLowerCase(), { id, x, y, w: ENTITY_W, h });
  });

  ast.relations.forEach((rel, idx) => {
    const a = entityBoxes.get(rel.from.toLowerCase());
    const b = entityBoxes.get(rel.to.toLowerCase());
    if (!a || !b) return;
    const startX = a.x + a.w;
    const startY = a.y + a.h / 2;
    const endX = b.x;
    const endY = b.y + b.h / 2;
    const arrowId = stableId(`rel:${rel.from}->${rel.to}:${idx}`);
    out.push(makeArrow(arrowId, startX, startY, endX, endY, a.id, b.id));
    const labelText = rel.cardinality && rel.label
      ? `${rel.cardinality} ${rel.label}`
      : rel.cardinality || rel.label;
    if (labelText) {
      const midX = (startX + endX) / 2 - 30;
      const midY = (startY + endY) / 2 - 10;
      out.push(
        makeText(
          stableId(`rel-label:${rel.from}->${rel.to}:${idx}`),
          midX,
          midY,
          80,
          18,
          labelText,
          12,
          'center',
        ),
      );
    }
  });

  return out;
}

function computeRowHeights(entities: DomainEntity[]): number[] {
  const heights: number[] = [];
  entities.forEach((e, idx) => {
    const row = Math.floor(idx / ENTITY_COLS);
    const h = ENTITY_HEADER_H + Math.max(1, e.attrs.length) * ATTR_LINE_H + 12;
    heights[row] = Math.max(heights[row] ?? 0, h);
  });
  return heights;
}

function sumRowHeights(rowHeights: number[], targetRow: number, gap: number): number {
  let y = 0;
  for (let r = 0; r < targetRow; r++) {
    y += (rowHeights[r] ?? 0) + gap;
  }
  return y;
}

// ---------- Element factories ----------

function baseElement(
  id: string,
  x: number,
  y: number,
  w: number,
  h: number,
): ExcalidrawElementBase {
  const seed = stableSeed(id);
  return {
    id,
    type: 'rectangle',
    x,
    y,
    width: w,
    height: h,
    angle: 0,
    strokeColor: '#1e1e1e',
    backgroundColor: 'transparent',
    fillStyle: 'solid',
    strokeWidth: 1,
    strokeStyle: 'solid',
    roughness: 1,
    opacity: 100,
    groupIds: [],
    frameId: null,
    roundness: { type: 3 },
    seed,
    versionNonce: seed ^ 0xa5a5,
    version: 1,
    isDeleted: false,
    boundElements: null,
    updated: 0,
    link: null,
    locked: false,
  };
}

function makeRect(
  id: string,
  x: number,
  y: number,
  w: number,
  h: number,
  stroke: string,
  fill: string,
  roundness: { type: number } | null = { type: 3 },
): RectElement {
  return {
    ...baseElement(id, x, y, w, h),
    type: 'rectangle',
    strokeColor: stroke,
    backgroundColor: fill,
    roundness,
  };
}

function makeText(
  id: string,
  x: number,
  y: number,
  w: number,
  h: number,
  text: string,
  fontSize: number,
  align: 'left' | 'center' | 'right',
): TextElement {
  return {
    ...baseElement(id, x, y, w, h),
    type: 'text',
    roundness: null,
    text,
    originalText: text,
    fontSize,
    fontFamily: 5, // Excalifont (default in Excalidraw 0.18)
    textAlign: align,
    verticalAlign: 'top',
    baseline: Math.round(fontSize * 0.85),
    containerId: null,
    lineHeight: 1.25,
    autoResize: false,
  };
}

function makeArrow(
  id: string,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  startId: string | null,
  endId: string | null,
): ArrowElement {
  const x = Math.min(x1, x2);
  const y = Math.min(y1, y2);
  const dx = x2 - x1;
  const dy = y2 - y1;
  return {
    ...baseElement(id, x, y, Math.max(1, Math.abs(dx)), Math.max(1, Math.abs(dy))),
    type: 'arrow',
    x: x1,
    y: y1,
    width: Math.max(1, Math.abs(dx)),
    height: Math.max(1, Math.abs(dy)),
    roundness: { type: 2 },
    points: [
      [0, 0],
      [dx, dy],
    ],
    lastCommittedPoint: null,
    startBinding: startId
      ? { elementId: startId, focus: 0, gap: 4 }
      : null,
    endBinding: endId ? { elementId: endId, focus: 0, gap: 4 } : null,
    startArrowhead: null,
    endArrowhead: 'arrow',
    elbowed: false,
  };
}

// ---------- Stable IDs / seeds ----------

function stableId(input: string): string {
  // Excalidraw element IDs are arbitrary strings; deterministic hashes keep
  // re-renders idempotent so the canvas doesn't spuriously animate.
  const h = fnv1a(input);
  return `gen-${h.toString(36)}`;
}

function stableSeed(input: string): number {
  return fnv1a(`seed:${input}`) >>> 0;
}

function fnv1a(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}
