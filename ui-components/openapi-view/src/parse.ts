/**
 * OpenAPI 3.x parser. Turns the raw YAML/JSON string into a flat,
 * UI-friendly model that the React tree can render without knowing
 * anything about $ref lookups, anyOf flattening, or schema recursion.
 *
 * Intentionally permissive — many of the generated specs in this app
 * are partial drafts. Missing fields default to sensible empty values;
 * unrecognised JSON-schema constructs degrade to `{ type: 'any' }`.
 */

import yaml from 'js-yaml';

export type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'HEAD' | 'OPTIONS';

export interface ParsedInfo {
  title: string;
  version: string;
  description: string;
}

export interface Param {
  name: string;
  type: string;
  required: boolean;
  /** "query" | "path" | "header" | "cookie" | "body" — kept as the raw `in` value. */
  in: string;
  desc: string;
}

export interface SchemaField {
  name: string;
  type: string;
  required: boolean;
  desc: string;
  enumValues?: string[];
  children?: SchemaField[];
}

export interface Schema {
  /** Display label — `"object"`, `"array<Charge>"`, `"string"`, … */
  type: string;
  fields: SchemaField[];
}

export interface Response {
  code: string;
  description: string;
  /** Display name when the response body resolves to a named schema. */
  schemaName?: string;
  schema?: Schema;
  example?: unknown;
}

export interface Operation {
  /** Stable id derived from method + path, used for anchors and React keys. */
  id: string;
  method: Method;
  path: string;
  /** One-line summary (used as the right-aligned label in the row). */
  name: string;
  /** Longer description shown inside the body. */
  summary: string;
  params: Param[];
  responses: Response[];
}

export interface TagSection {
  id: string;
  title: string;
  blurb: string;
  endpoints: Operation[];
}

export interface ParsedOpenApi {
  info: ParsedInfo;
  sections: TagSection[];
  schemas: Record<string, Schema>;
}

export interface ParseError {
  kind: 'parse-error';
  message: string;
}

export type ParseResult = ParsedOpenApi | ParseError;

const METHOD_SET = new Set<Method>(['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']);

// Crude JSON-pointer resolver — only needed for the local `#/components/...`
// shape that `$ref` always uses in our generated specs.
function resolveRef(root: unknown, ref: string): unknown {
  if (!ref.startsWith('#/')) return undefined;
  const parts = ref.slice(2).split('/');
  let cur: unknown = root;
  for (const p of parts) {
    if (!cur || typeof cur !== 'object') return undefined;
    cur = (cur as Record<string, unknown>)[decodeURIComponent(p.replace(/~1/g, '/').replace(/~0/g, '~'))];
  }
  return cur;
}

function asObject(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;
}

function asString(v: unknown, fallback = ''): string {
  return typeof v === 'string' ? v : fallback;
}

function asArray(v: unknown): unknown[] {
  return Array.isArray(v) ? v : [];
}

interface ResolveCtx {
  root: unknown;
  /** Set of refs currently being expanded — prevents infinite recursion on cyclic specs. */
  seen: Set<string>;
}

function describeSchemaType(node: Record<string, unknown>, ctx: ResolveCtx): string {
  const refStr = typeof node.$ref === 'string' ? node.$ref : undefined;
  if (refStr) {
    const lastSeg = refStr.split('/').pop() ?? 'ref';
    return lastSeg;
  }
  const type = asString(node.type);
  if (type === 'array') {
    const items = asObject(node.items);
    if (items) return `array<${describeSchemaType(items, ctx)}>`;
    return 'array';
  }
  if (Array.isArray(node.enum)) return 'enum';
  if (type) return type;
  if (node.properties) return 'object';
  if (node.allOf || node.oneOf || node.anyOf) return 'object';
  return 'any';
}

function buildField(
  name: string,
  node: Record<string, unknown>,
  required: boolean,
  ctx: ResolveCtx,
): SchemaField {
  // Resolve $ref to its target before reading the rest.
  let resolved = node;
  const refStr = typeof node.$ref === 'string' ? node.$ref : undefined;
  if (refStr && !ctx.seen.has(refStr)) {
    const target = asObject(resolveRef(ctx.root, refStr));
    if (target) {
      const nextSeen = new Set(ctx.seen);
      nextSeen.add(refStr);
      const lastSeg = refStr.split('/').pop() ?? 'ref';
      return {
        name,
        type: lastSeg,
        required,
        desc: asString(node.description, asString(target.description)),
        children: collectFields(target, { ...ctx, seen: nextSeen }),
      };
    }
  }

  const type = describeSchemaType(resolved, ctx);
  const desc = asString(resolved.description);
  const enumValues = Array.isArray(resolved.enum)
    ? resolved.enum.map((v) => String(v))
    : undefined;

  // Recurse for object-with-properties or array-with-object-items
  let children: SchemaField[] | undefined;
  if (resolved.properties) {
    children = collectFields(resolved, ctx);
  } else if (type.startsWith('array')) {
    const items = asObject(resolved.items);
    if (items?.properties) {
      children = collectFields(items, ctx);
    } else if (items && typeof items.$ref === 'string' && !ctx.seen.has(items.$ref)) {
      const target = asObject(resolveRef(ctx.root, items.$ref));
      if (target) {
        const nextSeen = new Set(ctx.seen);
        nextSeen.add(items.$ref);
        children = collectFields(target, { ...ctx, seen: nextSeen });
      }
    }
  }

  return {
    name,
    type,
    required,
    desc,
    enumValues,
    children,
  };
}

function collectFields(node: Record<string, unknown>, ctx: ResolveCtx): SchemaField[] {
  const props = asObject(node.properties);
  if (!props) return [];
  const requiredList = new Set(asArray(node.required).filter((v): v is string => typeof v === 'string'));
  const fields: SchemaField[] = [];
  for (const [name, raw] of Object.entries(props)) {
    const propNode = asObject(raw);
    if (!propNode) continue;
    fields.push(buildField(name, propNode, requiredList.has(name), ctx));
  }
  return fields;
}

function buildSchema(node: Record<string, unknown>, ctx: ResolveCtx): Schema {
  return {
    type: describeSchemaType(node, ctx),
    fields: collectFields(node, ctx),
  };
}

function bodyToSchemaAndName(
  body: Record<string, unknown> | undefined,
  ctx: ResolveCtx,
): { schema?: Schema; schemaName?: string; example?: unknown } {
  if (!body) return {};
  const content = asObject(body.content);
  if (!content) return {};
  // Pick the first JSON-ish media type we recognise.
  const mediaKey = Object.keys(content).find((k) => /json/i.test(k)) ?? Object.keys(content)[0];
  const media = asObject(content[mediaKey]);
  if (!media) return {};
  const schemaNode = asObject(media.schema);
  if (!schemaNode) return { example: media.example };

  const ref = typeof schemaNode.$ref === 'string' ? schemaNode.$ref : undefined;
  if (ref) {
    const target = asObject(resolveRef(ctx.root, ref));
    const name = ref.split('/').pop();
    return {
      schemaName: name,
      schema: target ? buildSchema(target, ctx) : undefined,
      example: media.example,
    };
  }
  return {
    schema: buildSchema(schemaNode, ctx),
    example: media.example,
  };
}

function buildParam(node: Record<string, unknown>, ctx: ResolveCtx): Param {
  const schemaNode = asObject(node.schema);
  return {
    name: asString(node.name),
    type: schemaNode ? describeSchemaType(schemaNode, ctx) : asString(node.type, 'string'),
    required: node.required === true,
    in: asString(node.in, 'query'),
    desc: asString(node.description),
  };
}

function buildOperation(
  method: Method,
  path: string,
  node: Record<string, unknown>,
  pathLevelParams: unknown[],
  ctx: ResolveCtx,
): Operation {
  const params: Param[] = [];
  for (const raw of [...pathLevelParams, ...asArray(node.parameters)]) {
    const obj = asObject(raw);
    if (obj) params.push(buildParam(obj, ctx));
  }

  // Body params: collapse `requestBody.content[…]/schema` into a synthetic
  // `body` row that lists every top-level property — closer to how Swagger
  // surfaces them than a single opaque "body" entry.
  const requestBody = asObject(node.requestBody);
  if (requestBody) {
    const { schema, schemaName } = bodyToSchemaAndName(requestBody, ctx);
    if (schema && schema.fields.length) {
      for (const f of schema.fields) {
        params.push({
          name: f.name,
          type: f.type,
          required: f.required,
          in: 'body',
          desc: f.desc,
        });
      }
    } else if (schemaName) {
      params.push({
        name: 'body',
        type: schemaName,
        required: requestBody.required === true,
        in: 'body',
        desc: asString(requestBody.description),
      });
    }
  }

  const responses: Response[] = [];
  const respObj = asObject(node.responses);
  if (respObj) {
    for (const [code, raw] of Object.entries(respObj)) {
      const r = asObject(raw);
      if (!r) continue;
      const { schema, schemaName, example } = bodyToSchemaAndName(r, ctx);
      responses.push({
        code,
        description: asString(r.description, ''),
        schemaName,
        schema,
        example,
      });
    }
  }

  return {
    id: `${method.toLowerCase()}-${path.replace(/[^a-z0-9]+/gi, '-').replace(/^-+|-+$/g, '')}`,
    method,
    path,
    name: asString(node.summary, asString(node.operationId, path)),
    summary: asString(node.description),
    params,
    responses,
  };
}

function buildSections(root: Record<string, unknown>, ctx: ResolveCtx): TagSection[] {
  const paths = asObject(root.paths) ?? {};
  const tags = asArray(root.tags).map((t) => asObject(t)).filter((t): t is Record<string, unknown> => !!t);
  const tagBlurb = new Map<string, string>();
  const tagOrder: string[] = [];
  for (const t of tags) {
    const name = asString(t.name);
    if (!name) continue;
    tagBlurb.set(name, asString(t.description));
    tagOrder.push(name);
  }

  const byTag = new Map<string, Operation[]>();
  for (const [path, raw] of Object.entries(paths)) {
    const pathNode = asObject(raw);
    if (!pathNode) continue;
    const pathLevelParams = asArray(pathNode.parameters);
    for (const [methodKey, opRaw] of Object.entries(pathNode)) {
      const method = methodKey.toUpperCase() as Method;
      if (!METHOD_SET.has(method)) continue;
      const opNode = asObject(opRaw);
      if (!opNode) continue;
      const op = buildOperation(method, path, opNode, pathLevelParams, ctx);
      const opTags = asArray(opNode.tags).filter((t): t is string => typeof t === 'string');
      const bucket = opTags[0] ?? 'Operations';
      if (!byTag.has(bucket)) byTag.set(bucket, []);
      byTag.get(bucket)!.push(op);
    }
  }

  // Preserve declared tag order; append any tags discovered only on operations.
  const seen = new Set<string>();
  const sections: TagSection[] = [];
  for (const t of tagOrder) {
    if (!byTag.has(t)) continue;
    seen.add(t);
    sections.push({
      id: slug(t),
      title: t,
      blurb: tagBlurb.get(t) ?? '',
      endpoints: byTag.get(t)!,
    });
  }
  for (const [t, ops] of byTag.entries()) {
    if (seen.has(t)) continue;
    sections.push({
      id: slug(t),
      title: t,
      blurb: tagBlurb.get(t) ?? '',
      endpoints: ops,
    });
  }
  return sections;
}

function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'section';
}

function buildSchemas(root: Record<string, unknown>, ctx: ResolveCtx): Record<string, Schema> {
  const components = asObject(root.components);
  if (!components) return {};
  const schemas = asObject(components.schemas);
  if (!schemas) return {};
  const out: Record<string, Schema> = {};
  for (const [name, raw] of Object.entries(schemas)) {
    const node = asObject(raw);
    if (!node) continue;
    out[name] = buildSchema(node, ctx);
  }
  return out;
}

export function parseOpenApi(text: string): ParseResult {
  let doc: unknown;
  try {
    doc = yaml.load(text);
  } catch (e) {
    return { kind: 'parse-error', message: e instanceof Error ? e.message : String(e) };
  }
  const root = asObject(doc);
  if (!root) {
    return { kind: 'parse-error', message: 'OpenAPI document is not an object' };
  }

  const ctx: ResolveCtx = { root, seen: new Set() };
  const info = asObject(root.info) ?? {};
  return {
    info: {
      title: asString(info.title, 'Untitled API'),
      version: asString(info.version, ''),
      description: asString(info.description),
    },
    sections: buildSections(root, ctx),
    schemas: buildSchemas(root, ctx),
  };
}
