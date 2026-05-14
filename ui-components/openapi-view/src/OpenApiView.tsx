import { useMemo, useState, type CSSProperties } from 'react';
import { parseOpenApi, type Method, type Operation, type Param, type Response, type Schema, type SchemaField, type TagSection } from './parse.js';
import './styles.css';

// ── Method colour tokens — ported from the prototype. The hue feeds an
// `--mh` custom property; the rest of the chip/border/bg colours come
// from CSS oklch() expressions in styles.css.
const METHOD_HUE: Record<Method, number> = {
  GET: 152,
  POST: 220,
  PUT: 32,
  PATCH: 280,
  DELETE: 12,
  HEAD: 200,
  OPTIONS: 200,
};

function methodVars(method: Method): CSSProperties {
  return { ['--mh' as string]: String(METHOD_HUE[method] ?? 200) } as CSSProperties;
}

const TYPE_HUE: Record<string, number> = {
  string: 152,
  integer: 220,
  number: 220,
  boolean: 280,
  timestamp: 220,
  enum: 60,
  object: 200,
  array: 200,
  hash: 200,
};
function typeHue(t: string): number {
  const base = t.replace(/<.*>$/, '').split('|')[0];
  if (base.startsWith('array')) return TYPE_HUE.array;
  return TYPE_HUE[base] ?? 200;
}

function typeStyle(t: string): CSSProperties {
  return { ['--type-hue' as string]: String(typeHue(t)) } as CSSProperties;
}

// ── JSON viewer ────────────────────────────────────────────────────────────
function JsonView({
  data,
  indent = 0,
  highlightKey,
  onHoverKey,
}: {
  data: unknown;
  indent?: number;
  highlightKey?: string | null;
  onHoverKey?: (k: string | null) => void;
}) {
  const pad = '  '.repeat(indent);
  if (data === null) return <span className="j-null">null</span>;
  if (typeof data === 'boolean') return <span className="j-bool">{String(data)}</span>;
  if (typeof data === 'number') return <span className="j-num">{data}</span>;
  if (typeof data === 'string') return <span className="j-str">&quot;{data}&quot;</span>;
  if (Array.isArray(data)) {
    if (data.length === 0) return <span className="j-pun">[]</span>;
    return (
      <>
        <span className="j-pun">[</span>{'\n'}
        {data.map((v, i) => (
          <span key={i}>
            {pad}{'  '}
            <JsonView data={v} indent={indent + 1} highlightKey={highlightKey} onHoverKey={onHoverKey} />
            {i < data.length - 1 ? <span className="j-pun">,</span> : null}
            {'\n'}
          </span>
        ))}
        {pad}<span className="j-pun">]</span>
      </>
    );
  }
  if (typeof data === 'object') {
    const entries = Object.entries(data as Record<string, unknown>);
    if (entries.length === 0) return <span className="j-pun">{'{}'}</span>;
    return (
      <>
        <span className="j-pun">{'{'}</span>{'\n'}
        {entries.map(([k, v], i) => (
          <span key={k}>
            {pad}{'  '}
            <span
              className={'j-key' + (highlightKey === k ? ' j-key-on' : '')}
              onMouseEnter={() => onHoverKey?.(k)}
              onMouseLeave={() => onHoverKey?.(null)}
            >
              &quot;{k}&quot;
            </span>
            <span className="j-pun">: </span>
            <JsonView data={v} indent={indent + 1} highlightKey={highlightKey} onHoverKey={onHoverKey} />
            {i < entries.length - 1 ? <span className="j-pun">,</span> : null}{'\n'}
          </span>
        ))}
        {pad}<span className="j-pun">{'}'}</span>
      </>
    );
  }
  return <span className="j-pun">{String(data)}</span>;
}

// ── Schema tree ────────────────────────────────────────────────────────────
function SchemaFieldRow({
  field,
  depth,
  hoveredKey,
  onHover,
}: {
  field: SchemaField;
  depth: number;
  hoveredKey: string | null;
  onHover: (k: string | null) => void;
}) {
  const hasChildren = !!(field.children && field.children.length);
  const [open, setOpen] = useState(depth < 1);
  const active = hoveredKey === field.name;
  return (
    <div
      className={'sch-field' + (active ? ' sch-active' : '')}
      style={{ ['--d' as string]: String(depth), ...typeStyle(field.type) } as CSSProperties}
      onMouseEnter={() => onHover(field.name)}
      onMouseLeave={() => onHover(null)}
    >
      <div className="sch-row">
        <div className="sch-gutter">
          {hasChildren ? (
            <button type="button" className="sch-toggle" data-open={open ? '1' : '0'} onClick={() => setOpen(!open)} aria-label="toggle">
              <svg viewBox="0 0 10 10" width="10" height="10">
                <path d="M2 4 L5 7 L8 4" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            </button>
          ) : (
            <span className="sch-dot" />
          )}
        </div>
        <div className="sch-meta">
          <div className="sch-head">
            <code className="sch-name">{field.name}</code>
            <span className="sch-type" style={typeStyle(field.type)}>{field.type}</span>
            {field.required && <span className="param-req">required</span>}
          </div>
          {field.desc && <div className="sch-desc">{field.desc}</div>}
          {field.enumValues && field.enumValues.length > 0 && (
            <div className="sch-enum">
              {field.enumValues.map((v) => (
                <code key={v}>{v}</code>
              ))}
            </div>
          )}
        </div>
      </div>
      {hasChildren && open && (
        <div className="sch-children">
          {field.children!.map((c) => (
            <SchemaFieldRow key={c.name} field={c} depth={depth + 1} hoveredKey={hoveredKey} onHover={onHover} />
          ))}
        </div>
      )}
    </div>
  );
}

function SchemaTree({ schema, hoveredKey, onHover }: { schema: Schema; hoveredKey: string | null; onHover: (k: string | null) => void }) {
  return (
    <div className="sch-tree">
      {schema.fields.length === 0 ? (
        <div style={{ padding: '12px 14px', fontSize: 13, color: 'var(--fg-soft)' }}>
          No fields declared.
        </div>
      ) : (
        schema.fields.map((f) => (
          <SchemaFieldRow key={f.name} field={f} depth={0} hoveredKey={hoveredKey} onHover={onHover} />
        ))
      )}
    </div>
  );
}

// ── Parameters table ───────────────────────────────────────────────────────
function ParamsTable({ params }: { params: Param[] }) {
  if (!params.length) return <div className="params-empty">No parameters</div>;
  return (
    <div className="params" role="table">
      <div className="params-head" role="row">
        <div role="columnheader">Name</div>
        <div role="columnheader">Type</div>
        <div role="columnheader">Description</div>
      </div>
      {params.map((p, i) => (
        <div className="param-row" role="row" key={`${p.in}-${p.name}-${i}`}>
          <div className="param-name-col">
            <code className="param-name">{p.name}</code>
            {p.required && <span className="param-req">required</span>}
          </div>
          <div className="param-type-col">
            <span className="sch-type" style={typeStyle(p.type)}>{p.type}</span>
          </div>
          <div className="param-desc-col">{p.desc}</div>
        </div>
      ))}
    </div>
  );
}

// ── Response row ───────────────────────────────────────────────────────────
function statusKind(code: string): 'ok' | 'warn' | 'err' | 'info' {
  const n = Number(code);
  if (n >= 200 && n < 300) return 'ok';
  if (n >= 400 && n < 500) return 'warn';
  if (n >= 500) return 'err';
  return 'info';
}

function ResponseRow({ code, description, schema, schemaName, example }: Response) {
  const [open, setOpen] = useState(code === '200' || code === 'default');
  const [hoverKey, setHoverKey] = useState<string | null>(null);
  const [tab, setTab] = useState<'schema' | 'example'>(schema ? 'schema' : 'example');
  const hasContent = !!(schema || example !== undefined);
  return (
    <div className={'resp-row' + (open ? ' is-open' : '')} data-kind={statusKind(code)}>
      <button type="button" className="resp-head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="resp-code">{code}</span>
        <span className="resp-desc">{description || (schemaName ? `Returns ${schemaName}` : '')}</span>
        {schemaName && <code className="resp-ref">{schemaName}</code>}
        <span className="resp-chev" data-open={open ? '1' : '0'} aria-hidden>
          <svg viewBox="0 0 10 10" width="10" height="10"><path d="M2 4 L5 7 L8 4" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" /></svg>
        </span>
      </button>
      {open && hasContent && (
        <div className="resp-body">
          <div className="resp-tabs" role="tablist">
            {schema && (
              <button type="button" role="tab" aria-selected={tab === 'schema'} className={tab === 'schema' ? 'is-active' : ''} onClick={() => setTab('schema')}>
                Schema
              </button>
            )}
            {example !== undefined && (
              <button type="button" role="tab" aria-selected={tab === 'example'} className={tab === 'example' ? 'is-active' : ''} onClick={() => setTab('example')}>
                Example
              </button>
            )}
            <div className="resp-tab-meta">application/json</div>
          </div>
          {tab === 'schema' && schema && <SchemaTree schema={schema} hoveredKey={hoverKey} onHover={setHoverKey} />}
          {tab === 'example' && example !== undefined && (
            <pre className="code-block"><code><JsonView data={example} /></code></pre>
          )}
        </div>
      )}
    </div>
  );
}

// ── Operation row ──────────────────────────────────────────────────────────
function OperationRow({ op }: { op: Operation }) {
  const [open, setOpen] = useState(false);
  return (
    <article className={'op' + (open ? ' is-open' : '')} data-method={op.method} style={methodVars(op.method)}>
      <button type="button" className="op-head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="op-method">{op.method}</span>
        <code className="op-path">{op.path}</code>
        <span className="op-summary">{op.name}</span>
        <span className="op-chev" data-open={open ? '1' : '0'} aria-hidden>
          <svg viewBox="0 0 10 10" width="10" height="10"><path d="M2 4 L5 7 L8 4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" /></svg>
        </span>
      </button>
      {open && (
        <div className="op-body">
          {op.summary && <p className="op-desc">{op.summary}</p>}
          <section className="op-sect">
            <header className="op-sect-head">
              <h4>Parameters</h4>
              <span className="op-sect-meta">{op.params.length}</span>
            </header>
            <ParamsTable params={op.params} />
          </section>
          <section className="op-sect">
            <header className="op-sect-head">
              <h4>Responses</h4>
              <span className="op-sect-meta">{op.responses.length}</span>
            </header>
            <div className="resp-list">
              {op.responses.length === 0 ? (
                <div className="params-empty">No responses declared</div>
              ) : (
                op.responses.map((r) => <ResponseRow key={r.code} {...r} />)
              )}
            </div>
          </section>
        </div>
      )}
    </article>
  );
}

// ── Tag section ────────────────────────────────────────────────────────────
function TagSectionView({ section }: { section: TagSection }) {
  return (
    <section className="tag-section">
      <header className="tag-head">
        <h2 className="tag-title">{section.title}</h2>
        {section.blurb && <p className="tag-blurb">{section.blurb}</p>}
      </header>
      <div className="op-list">
        {section.endpoints.map((ep) => (
          <OperationRow key={ep.id} op={ep} />
        ))}
      </div>
    </section>
  );
}

// ── Schemas (models) section ───────────────────────────────────────────────
function SchemaCard({ name, schema }: { name: string; schema: Schema }) {
  const [open, setOpen] = useState(false);
  const [hoverKey, setHoverKey] = useState<string | null>(null);
  return (
    <article className={'model' + (open ? ' is-open' : '')}>
      <button type="button" className="model-head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <code className="model-name">{name}</code>
        <span className="model-type">{schema.type}</span>
        <span className="model-fields">
          {schema.fields.length} field{schema.fields.length === 1 ? '' : 's'}
        </span>
        <span className="op-chev" data-open={open ? '1' : '0'} aria-hidden>
          <svg viewBox="0 0 10 10" width="10" height="10"><path d="M2 4 L5 7 L8 4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" /></svg>
        </span>
      </button>
      {open && (
        <div className="model-body">
          <SchemaTree schema={schema} hoveredKey={hoverKey} onHover={setHoverKey} />
        </div>
      )}
    </article>
  );
}

function SchemasSection({ schemas }: { schemas: Record<string, Schema> }) {
  const names = Object.keys(schemas);
  if (names.length === 0) return null;
  return (
    <section className="tag-section">
      <header className="tag-head">
        <h2 className="tag-title">Schemas</h2>
        <p className="tag-blurb">Reusable object definitions referenced throughout the API.</p>
      </header>
      <div className="op-list">
        {names.map((n) => (
          <SchemaCard key={n} name={n} schema={schemas[n]} />
        ))}
      </div>
    </section>
  );
}

// ── Public component ───────────────────────────────────────────────────────
export interface OpenApiViewProps {
  /** Raw OpenAPI YAML or JSON text. */
  spec: string;
}

export function OpenApiView({ spec }: OpenApiViewProps) {
  const parsed = useMemo(() => parseOpenApi(spec), [spec]);
  const [query, setQuery] = useState('');

  if ('kind' in parsed) {
    return (
      <div className="oapi">
        <div className="oapi-doc">
          <div className="oapi-error">
            <h3>Couldn't parse the OpenAPI document</h3>
            <div>{parsed.message}</div>
          </div>
        </div>
      </div>
    );
  }

  // Live filter — keep an operation if its path / method / summary or
  // the section title matches the query.
  const filtered = (() => {
    const q = query.trim().toLowerCase();
    if (!q) return parsed.sections;
    return parsed.sections
      .map((s) => {
        if (s.title.toLowerCase().includes(q)) return s;
        const eps = s.endpoints.filter(
          (ep) =>
            ep.path.toLowerCase().includes(q) ||
            ep.name.toLowerCase().includes(q) ||
            ep.method.toLowerCase().includes(q),
        );
        return eps.length ? { ...s, endpoints: eps } : null;
      })
      .filter((s): s is TagSection => !!s);
  })();

  return (
    <div className="oapi">
      <div className="oapi-doc">
      <section className="hero">
        <div className="hero-eyebrow">
          <span className="status-dot" />
          <span>OPENAPI</span>
          {parsed.info.version && (
            <>
              <span className="hero-eyebrow-sep" />
              <span>{parsed.info.version}</span>
            </>
          )}
        </div>
        <h1 className="hero-title">{parsed.info.title}</h1>
        {parsed.info.description && <p className="hero-lede">{parsed.info.description}</p>}
      </section>

      <div className="toolbar">
        <div className="search">
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden>
            <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.4" />
            <path d="M10.5 10.5 L14 14" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
          </svg>
          <input
            type="search"
            placeholder="Filter operations · path, method, or summary"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button type="button" className="search-clear" onClick={() => setQuery('')} aria-label="Clear">
              <svg viewBox="0 0 12 12" width="11" height="11"><path d="M3 3 L9 9 M9 3 L3 9" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" /></svg>
            </button>
          )}
        </div>
      </div>

      <div className="sections">
        {filtered.map((s) => (
          <TagSectionView key={s.id} section={s} />
        ))}
        {filtered.length === 0 && (
          <div className="empty">No operations match &ldquo;{query}&rdquo;.</div>
        )}
        <SchemasSection schemas={parsed.schemas} />
      </div>
      </div>
    </div>
  );
}
