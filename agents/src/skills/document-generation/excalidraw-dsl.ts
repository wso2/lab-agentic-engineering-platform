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

interface PointBinding {
  elementId: string;
  focus: number;
  gap: number;
  /** Normalized [x, y] in [0, 1] relative to the bound element. Required
   *  for elbow arrows (FixedPointBinding); harmless on straight arrows. */
  fixedPoint?: [number, number];
}

interface ArrowElement extends ExcalidrawElementBase {
  type: 'arrow' | 'line';
  points: [number, number][];
  lastCommittedPoint: null;
  startBinding: PointBinding | null;
  endBinding: PointBinding | null;
  startArrowhead: null | 'arrow';
  endArrowhead: null | 'arrow';
  elbowed?: boolean;
  /** Elbow-arrow specific. Null lets Excalidraw compute segments itself. */
  fixedSegments?: null;
  startIsSpecial?: null;
  endIsSpecial?: null;
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

// Accent color for flow markers + screen number badges. Picked to read
// against the standard wireframe palette without competing with element
// fills.
const FLOW_ACCENT = '#1971c2';

interface ButtonAnchor {
  /** Last button rect's right edge X (absolute canvas coords). */
  rightX: number;
  /** Button's vertical midpoint Y. */
  midY: number;
}

function renderWireframes(ast: WireframeAst): ExcalidrawElement[] {
  const out: ExcalidrawElement[] = [];
  // Screen-name → 1-based number for `→(N)` flow markers.
  const screenNumber = new Map<string, number>();
  // Screen-name → last-button anchor (right edge of the screen's final
  // button). Used as the attach point for `→(N)` markers; if absent the
  // marker falls back to the screen header row.
  const screenAnchor = new Map<string, ButtonAnchor>();
  // Screen-name → header anchor (always present), so flows from screens
  // with no button still surface as a marker beside the screen title.
  const screenHeaderAnchor = new Map<string, ButtonAnchor>();

  ast.screens.forEach((screen, idx) => {
    const number = idx + 1;
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
    // Numbered badge in the top-right corner so flow markers (`→(N)`)
    // are immediately resolvable to a screen.
    out.push(
      withColor(
        makeText(
          stableId(`screen-num:${screen.name}:${idx}`),
          sx + SCREEN_W - 44,
          sy + 8,
          32,
          18,
          `(${number})`,
          14,
          'right',
        ),
        FLOW_ACCENT,
      ),
    );
    screenNumber.set(screen.name.toLowerCase(), number);
    screenHeaderAnchor.set(screen.name.toLowerCase(), {
      rightX: sx + SCREEN_W - 56,
      midY: sy + 12,
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
          // Each button updates the anchor; the last one wins, which
          // matches the heuristic "the bottom-most CTA drives the flow".
          screenAnchor.set(screen.name.toLowerCase(), {
            rightX: ex + el.width,
            midY: ey + el.height / 2 - 8,
          });
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

  // Flow markers: render `→(N)` next to the source screen's last button
  // (or the screen header if no button exists). No arrow lines — keeps
  // the canvas free of cross-screen overlap.
  for (const [i, flow] of ast.flows.entries()) {
    const targetNum = screenNumber.get(flow.to.toLowerCase());
    if (targetNum === undefined) continue;
    const anchor =
      screenAnchor.get(flow.from.toLowerCase()) ??
      screenHeaderAnchor.get(flow.from.toLowerCase());
    if (!anchor) continue;
    out.push(
      withColor(
        makeText(
          stableId(`flow-marker:${flow.from}->${flow.to}:${i}`),
          anchor.rightX + 8,
          anchor.midY,
          56,
          18,
          `→(${targetNum})`,
          13,
          'left',
        ),
        FLOW_ACCENT,
      ),
    );
  }

  return out;
}

/** Override an element's stroke colour without mutating the input. */
function withColor<T extends ExcalidrawElementBase>(el: T, color: string): T {
  return { ...el, strokeColor: color };
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

// ---------- Domain model layout (Sugiyama-style) ----------
//
// The renderer used to slot entities into a fixed 3-column grid and draw
// straight arrows edge-to-edge between them, which produced lines that
// crossed unrelated entity boxes whenever the graph topology didn't
// happen to match the grid. The layered layout below:
//
//   1. Breaks cycles by greedy DFS (back-edges flagged, not drawn straight)
//   2. Assigns layers via longest-path topological sort
//   3. Reorders within each layer using barycenter sweeps (up to 24)
//   4. Places coordinates with each layer centred horizontally
//
// Edges are then rendered as elbowed arrows so Excalidraw routes them
// between rows of entities. Re-adding an entity is automatically picked
// up because the layout is a pure function of the parsed AST.

const ENTITY_BACK_EDGE_COLOR = '#868e96';
const ENTITY_FWD_EDGE_COLOR = '#1e1e1e';

interface DomainLayoutNode {
  name: string;
  id: string;
  layer: number;
  order: number;
  x: number;
  y: number;
  w: number;
  h: number;
}

interface DomainLayoutEdge {
  from: string;
  to: string;
  kind: 'forward' | 'back';
  relIdx: number;
}

interface DomainLayout {
  nodes: Map<string, DomainLayoutNode>;
  edges: DomainLayoutEdge[];
}

function entityHeight(e: DomainEntity): number {
  return ENTITY_HEADER_H + Math.max(1, e.attrs.length) * ATTR_LINE_H + 12;
}

export function layoutDomainModel(ast: DomainAst): DomainLayout {
  const nodeNames = ast.entities.map((e) => e.name.toLowerCase());
  const nodeIndexByName = new Map(nodeNames.map((n, i) => [n, i]));
  const entityByName = new Map(ast.entities.map((e) => [e.name.toLowerCase(), e]));

  // Filter relations to those with known endpoints.
  const relList = ast.relations
    .map((rel, idx) => ({
      from: rel.from.toLowerCase(),
      to: rel.to.toLowerCase(),
      idx,
    }))
    .filter((r) => nodeIndexByName.has(r.from) && nodeIndexByName.has(r.to));

  // 1. Detect back edges via DFS (greedy cycle-break).
  const tempAdj = new Map<string, string[]>();
  for (const n of nodeNames) tempAdj.set(n, []);
  for (const r of relList) tempAdj.get(r.from)!.push(r.to);

  const VISITING = 1;
  const VISITED = 2;
  const dfsState = new Map<string, number>();
  const backEdgeKeys = new Set<string>();

  const dfs = (node: string): void => {
    dfsState.set(node, VISITING);
    for (const to of tempAdj.get(node)!) {
      const s = dfsState.get(to);
      if (s === VISITING) {
        backEdgeKeys.add(`${node}->${to}`);
      } else if (s !== VISITED) {
        dfs(to);
      }
    }
    dfsState.set(node, VISITED);
  };
  for (const n of nodeNames) if (!dfsState.has(n)) dfs(n);

  const forwardEdges: typeof relList = [];
  const backEdges: typeof relList = [];
  for (const r of relList) {
    if (backEdgeKeys.has(`${r.from}->${r.to}`)) backEdges.push(r);
    else forwardEdges.push(r);
  }

  // 2. Longest-path layering over forward edges only.
  const fwdAdj = new Map<string, string[]>();
  const fwdInDeg = new Map<string, number>();
  for (const n of nodeNames) {
    fwdAdj.set(n, []);
    fwdInDeg.set(n, 0);
  }
  for (const e of forwardEdges) {
    fwdAdj.get(e.from)!.push(e.to);
    fwdInDeg.set(e.to, (fwdInDeg.get(e.to) ?? 0) + 1);
  }
  const layer = new Map<string, number>();
  const remaining = new Map(fwdInDeg);
  const queue: string[] = [];
  for (const n of nodeNames) {
    if ((remaining.get(n) ?? 0) === 0) {
      layer.set(n, 0);
      queue.push(n);
    }
  }
  while (queue.length) {
    const node = queue.shift()!;
    const lvl = layer.get(node) ?? 0;
    for (const to of fwdAdj.get(node)!) {
      const next = Math.max(layer.get(to) ?? 0, lvl + 1);
      layer.set(to, next);
      const rem = (remaining.get(to) ?? 0) - 1;
      remaining.set(to, rem);
      if (rem === 0) queue.push(to);
    }
  }
  // Fallback for any node not yet placed (shouldn't happen after cycle
  // break, but stays robust against malformed AST).
  for (const n of nodeNames) if (!layer.has(n)) layer.set(n, 0);

  // 3. Bucket into layers, seed order by AST appearance.
  const maxLayer = Math.max(0, ...Array.from(layer.values()));
  const layerNodes: string[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const n of nodeNames) layerNodes[layer.get(n)!]!.push(n);
  for (const ln of layerNodes) {
    ln.sort((a, b) => (nodeIndexByName.get(a)! - nodeIndexByName.get(b)!));
  }

  // 4. Barycenter sweeps — 24 iterations or until stable.
  const orderOf = new Map<string, number>();
  const refreshOrder = () => {
    for (const ln of layerNodes) ln.forEach((n, i) => orderOf.set(n, i));
  };
  refreshOrder();

  const arraysEqual = (a: string[], b: string[]) => {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
    return true;
  };

  for (let iter = 0; iter < 24; iter++) {
    let changed = false;
    // Down sweep — order each layer L (L>0) by avg position in layer L-1.
    for (let l = 1; l < layerNodes.length; l++) {
      const next = [...layerNodes[l]!];
      const baryFor = (node: string): number => {
        const orders: number[] = [];
        for (const e of forwardEdges) {
          if (e.to === node && layer.get(e.from) === l - 1) {
            orders.push(orderOf.get(e.from) ?? 0);
          }
        }
        if (orders.length === 0) return orderOf.get(node) ?? 0;
        return orders.reduce((a, b) => a + b, 0) / orders.length;
      };
      next.sort((a, b) => {
        const diff = baryFor(a) - baryFor(b);
        if (diff !== 0) return diff;
        return (orderOf.get(a) ?? 0) - (orderOf.get(b) ?? 0);
      });
      if (!arraysEqual(next, layerNodes[l]!)) {
        layerNodes[l] = next;
        changed = true;
      }
    }
    refreshOrder();
    // Up sweep.
    for (let l = layerNodes.length - 2; l >= 0; l--) {
      const next = [...layerNodes[l]!];
      const baryFor = (node: string): number => {
        const orders: number[] = [];
        for (const e of forwardEdges) {
          if (e.from === node && layer.get(e.to) === l + 1) {
            orders.push(orderOf.get(e.to) ?? 0);
          }
        }
        if (orders.length === 0) return orderOf.get(node) ?? 0;
        return orders.reduce((a, b) => a + b, 0) / orders.length;
      };
      next.sort((a, b) => {
        const diff = baryFor(a) - baryFor(b);
        if (diff !== 0) return diff;
        return (orderOf.get(a) ?? 0) - (orderOf.get(b) ?? 0);
      });
      if (!arraysEqual(next, layerNodes[l]!)) {
        layerNodes[l] = next;
        changed = true;
      }
    }
    refreshOrder();
    if (!changed) break;
  }

  // 5. Place coordinates — each layer is centred horizontally relative to
  // the widest layer.
  const layerHeights = layerNodes.map((ln) => {
    let h = 0;
    for (const n of ln) {
      const e = entityByName.get(n)!;
      h = Math.max(h, entityHeight(e));
    }
    return h;
  });
  const layerY: number[] = [];
  let yCursor = 0;
  for (let l = 0; l < layerNodes.length; l++) {
    layerY.push(yCursor);
    yCursor += layerHeights[l]! + ENTITY_GAP_Y;
  }
  const widthOfLayer = (count: number) =>
    count > 0 ? count * ENTITY_W + (count - 1) * ENTITY_GAP_X : 0;
  const maxWidth = Math.max(0, ...layerNodes.map((ln) => widthOfLayer(ln.length)));

  const nodes = new Map<string, DomainLayoutNode>();
  for (let l = 0; l < layerNodes.length; l++) {
    const ln = layerNodes[l]!;
    const offset = (maxWidth - widthOfLayer(ln.length)) / 2;
    for (let i = 0; i < ln.length; i++) {
      const name = ln[i]!;
      const e = entityByName.get(name)!;
      const h = entityHeight(e);
      const x = offset + i * (ENTITY_W + ENTITY_GAP_X);
      const y = layerY[l]! + (layerHeights[l]! - h) / 2;
      nodes.set(name, {
        name: e.name,
        id: stableId(`entity:${e.name}:${nodeIndexByName.get(name)}`),
        layer: l,
        order: i,
        x,
        y,
        w: ENTITY_W,
        h,
      });
    }
  }

  // 6. Edges list, preserving DSL declaration order.
  const edges: DomainLayoutEdge[] = relList.map((r) => ({
    from: r.from,
    to: r.to,
    kind: backEdgeKeys.has(`${r.from}->${r.to}`) ? 'back' : 'forward',
    relIdx: r.idx,
  }));

  return { nodes, edges };
}

function renderDomainModel(ast: DomainAst): ExcalidrawElement[] {
  const out: ExcalidrawElement[] = [];
  const layout = layoutDomainModel(ast);

  // Entities — same visual recipe as before, just positioned by the
  // layered layout instead of a fixed grid.
  for (const entity of ast.entities) {
    const node = layout.nodes.get(entity.name.toLowerCase());
    if (!node) continue;
    out.push(makeRect(node.id, node.x, node.y, node.w, node.h, '#1e1e1e', '#fff9db'));
    out.push(
      makeText(
        stableId(`entity-name:${entity.name}:${node.layer}-${node.order}`),
        node.x + 12,
        node.y + 8,
        node.w - 24,
        20,
        entity.name,
        16,
        'left',
      ),
    );
    entity.attrs.forEach((attr, ai) => {
      out.push(
        makeText(
          stableId(`attr:${entity.name}:${attr.name}:${ai}`),
          node.x + 12,
          node.y + ENTITY_HEADER_H + ai * ATTR_LINE_H + 4,
          node.w - 24,
          ATTR_LINE_H - 4,
          `${attr.name}: ${attr.type}`,
          13,
          'left',
        ),
      );
    });
  }

  // Relations — elbow-routed arrows. Each arrow chooses the closest pair
  // of faces (top/bottom/left/right) on source and target based on their
  // relative position, so the routing exits and enters whichever side
  // points roughly at the other entity. Forward and back edges share the
  // same routing; back edges only differ in colour.
  for (const edge of layout.edges) {
    const a = layout.nodes.get(edge.from);
    const b = layout.nodes.get(edge.to);
    if (!a || !b) continue;
    const arrowId = stableId(`rel:${edge.from}->${edge.to}:${edge.relIdx}`);
    const rel = ast.relations[edge.relIdx]!;
    const { start, end } = chooseFaces(a, b);
    const color = edge.kind === 'back' ? ENTITY_BACK_EDGE_COLOR : ENTITY_FWD_EDGE_COLOR;
    out.push(
      makeStraightArrow(arrowId, start.x, start.y, end.x, end.y, a.id, b.id, {
        strokeColor: color,
      }),
    );
    const labelText = relationLabel(rel);
    if (labelText) {
      const pos = labelPositionFor(start, end);
      out.push(
        withColor(
          makeText(
            stableId(`rel-label:${edge.from}->${edge.to}:${edge.relIdx}`),
            pos.x,
            pos.y,
            96,
            16,
            truncateLabel(labelText, 24),
            12,
            pos.align,
          ),
          color,
        ),
      );
    }
  }

  return out;
}

interface EdgeAnchor {
  x: number;
  y: number;
  /** Normalised attach point on the bound element ([x,y] in [0,1]). */
  fixedPoint: [number, number];
  /** Which face the anchor sits on. Used to position the label clear of
   *  the entity rectangle. */
  face: 'top' | 'bottom' | 'left' | 'right';
}

/**
 * Pick the closest pair of faces between two rectangles. Uses the gap
 * between rectangles (not the centre-to-centre vector) so two entities
 * sitting in different rows always exit through top/bottom even when
 * they're horizontally offset within their row — the row-to-row distance
 * is what reads as "vertical" to the eye, not the centre angle.
 */
function chooseFaces(
  a: { x: number; y: number; w: number; h: number },
  b: { x: number; y: number; w: number; h: number },
): { start: EdgeAnchor; end: EdgeAnchor } {
  const cax = a.x + a.w / 2;
  const cay = a.y + a.h / 2;
  const cbx = b.x + b.w / 2;
  const cby = b.y + b.h / 2;
  const dx = cbx - cax;
  const dy = cby - cay;
  // Edge-to-edge gaps. Positive when rectangles don't overlap on that
  // axis; negative when they overlap. We pick the axis with the larger
  // non-overlap so the arrow exits the face that's already pointing
  // toward empty space.
  const verticalGap =
    dy >= 0 ? b.y - (a.y + a.h) : a.y - (b.y + b.h);
  const horizontalGap =
    dx >= 0 ? b.x - (a.x + a.w) : a.x - (b.x + b.w);
  // If rectangles are stacked in different rows (verticalGap > 0) AND
  // not too far apart horizontally relative to the row gap, prefer
  // vertical exits — it reads as "Y depends on X" in the layout.
  const stacked = verticalGap > 0 && verticalGap >= horizontalGap * 0.3;
  if (stacked) {
    if (dy >= 0) {
      return {
        start: { x: cax, y: a.y + a.h, fixedPoint: [0.5, 1], face: 'bottom' },
        end: { x: cbx, y: b.y, fixedPoint: [0.5, 0], face: 'top' },
      };
    }
    return {
      start: { x: cax, y: a.y, fixedPoint: [0.5, 0], face: 'top' },
      end: { x: cbx, y: b.y + b.h, fixedPoint: [0.5, 1], face: 'bottom' },
    };
  }
  if (dx >= 0) {
    return {
      start: { x: a.x + a.w, y: cay, fixedPoint: [1, 0.5], face: 'right' },
      end: { x: b.x, y: cby, fixedPoint: [0, 0.5], face: 'left' },
    };
  }
  return {
    start: { x: a.x, y: cay, fixedPoint: [0, 0.5], face: 'left' },
    end: { x: b.x + b.w, y: cby, fixedPoint: [1, 0.5], face: 'right' },
  };
}

/**
 * Position the relation label clear of both entities. Sits beside the
 * source's exit face so it reads as "this entity is the origin of the
 * relation". For vertical exits the label drops below the start point;
 * for horizontal exits it sits to the right.
 */
function labelPositionFor(
  start: EdgeAnchor,
  end: EdgeAnchor,
): { x: number; y: number; align: 'left' | 'center' | 'right' } {
  if (start.face === 'bottom' || start.face === 'top') {
    // Vertical exit — place label just outside the start face, slightly
    // toward the target so it sits over the elbow's vertical leg.
    const x = start.x + (end.x - start.x) * 0.15 - 48;
    const yOffset = start.face === 'bottom' ? 6 : -22;
    return { x, y: start.y + yOffset, align: 'center' };
  }
  // Horizontal exit — place label above the start point, beside the
  // entity it belongs to.
  const xOffset = start.face === 'right' ? 8 : -104;
  return {
    x: start.x + xOffset,
    y: start.y - 22,
    align: start.face === 'right' ? 'left' : 'right',
  };
}

function relationLabel(rel: DomainRelation): string {
  if (rel.cardinality && rel.label) return `${rel.cardinality} ${rel.label}`;
  return rel.cardinality || rel.label || '';
}

function truncateLabel(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
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

/**
 * Build a straight arrow between two bound elements. We previously used
 * elbow arrows here, but Excalidraw's elbow router computes its bends
 * from the bound endpoints alone — it doesn't pathfind around other
 * entities, so for diagonally placed entities the Z-shape ended up
 * crossing whichever rectangle sat between source and target. A straight
 * line from the closest face on the source to the closest face on the
 * target reads more like a direct relation and follows the
 * "attach to the nearest edge" intent.
 */
function makeStraightArrow(
  id: string,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  startId: string | null,
  endId: string | null,
  options: { strokeColor?: string } = {},
): ArrowElement {
  const dx = x2 - x1;
  const dy = y2 - y1;
  const strokeColor = options.strokeColor ?? '#1e1e1e';
  return {
    ...baseElement(id, x1, y1, Math.max(1, Math.abs(dx)), Math.max(1, Math.abs(dy))),
    type: 'arrow',
    x: x1,
    y: y1,
    width: Math.max(1, Math.abs(dx)),
    height: Math.max(1, Math.abs(dy)),
    strokeColor,
    roundness: { type: 2 },
    points: [
      [0, 0],
      [dx, dy],
    ],
    lastCommittedPoint: null,
    startBinding: startId ? { elementId: startId, focus: 0, gap: 4 } : null,
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
