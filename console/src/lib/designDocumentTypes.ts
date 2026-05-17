// Registry of document types supported in the design directory. Mirrors
// `documentTypes.ts` for requirements, but the design tree is nested:
// the root carries `design.md`, and per-component files live under
// `components/<name>/`.

export type DesignDocumentTypeId =
  | 'system-design'
  | 'component-design'
  | 'component-openapi';

export interface DesignDocumentType {
  id: DesignDocumentTypeId;
  label: string;
  description: string;
  /** Path relative to `.asdlc/design/`. May contain `<name>` placeholder for per-component files. */
  pathTemplate: string;
  extension: string;
  /** True when only one file of this type is allowed (e.g. root design.md). */
  unique: boolean;
  /** True when the file cannot be deleted via the UI. */
  protected?: boolean;
  /** ID of an agents-service document-generation skill, if any. */
  generationSkillId?: string;
  /**
   * How to compute source files for the generation skill. Receives the
   * current bundle file map + the target path; returns a Record<filename,
   * content> the skill expects (filenames are the keys the skill reads;
   * for design skills these are paths relative to .asdlc/design/).
   */
  generationSources?: (
    files: Record<string, string>,
    targetPath: string,
  ) => Record<string, string>;
}

export const DESIGN_DOCUMENT_TYPES: DesignDocumentType[] = [
  {
    id: 'system-design',
    label: 'System Design',
    description: 'System-level architecture overview (root design.md).',
    pathTemplate: 'design.md',
    extension: '.md',
    unique: true,
    protected: true,
    // No generation skill for the root — it's part of the whole-design
    // architect generation. The "Regenerate" button on the page invokes
    // `POST /design/generate` which redoes the whole tree.
  },
  {
    id: 'component-design',
    label: 'Component Design',
    description:
      'Per-component design.md (frontmatter + prose). Regenerated from the system design and sibling components.',
    pathTemplate: 'components/<name>/design.md',
    extension: '.md',
    unique: false,
    generationSkillId: 'component-design',
    generationSources: (files, targetPath) => {
      // Sources: root design.md + every OTHER component's design.md.
      const out: Record<string, string> = {};
      const root = files['design.md'];
      if (root != null) out['design.md'] = root;
      const targetPrefix = targetPath.replace(/\/design\.md$/, '/');
      for (const [path, content] of Object.entries(files)) {
        if (
          path.startsWith('components/') &&
          path.endsWith('/design.md') &&
          !path.startsWith(targetPrefix)
        ) {
          out[path] = content;
        }
      }
      return out;
    },
  },
  {
    id: 'component-openapi',
    label: 'Component OpenAPI',
    description:
      'Per-component OpenAPI 3.0.3 spec. Regenerated from the matching design.md.',
    pathTemplate: 'components/<name>/openapi.yaml',
    extension: '.yaml',
    unique: false,
    generationSkillId: 'component-openapi',
    generationSources: (files, targetPath) => {
      // Source: the adjacent design.md.
      const designPath = targetPath.replace(/\/openapi\.yaml$/, '/design.md');
      const out: Record<string, string> = {};
      const designMd = files[designPath];
      if (designMd != null) out[designPath] = designMd;
      return out;
    },
  },
];

const DESIGN_DOCUMENT_TYPES_BY_ID = new Map<DesignDocumentTypeId, DesignDocumentType>(
  DESIGN_DOCUMENT_TYPES.map((t) => [t.id, t]),
);

export function getDesignDocumentType(
  id: DesignDocumentTypeId | string,
): DesignDocumentType | undefined {
  return DESIGN_DOCUMENT_TYPES_BY_ID.get(id as DesignDocumentTypeId);
}

/**
 * Match a path (relative to `.asdlc/design/`) to its design document type.
 */
export function designDocumentTypeForPath(path: string): DesignDocumentType | undefined {
  if (path === 'design.md') return getDesignDocumentType('system-design');
  if (/^components\/[^/]+\/design\.md$/.test(path)) return getDesignDocumentType('component-design');
  if (/^components\/[^/]+\/openapi\.ya?ml$/.test(path)) return getDesignDocumentType('component-openapi');
  return undefined;
}

/**
 * Extract the component name from a per-component design path. Returns
 * undefined for the root or paths that aren't under `components/<name>/`.
 */
export function componentNameFromPath(path: string): string | undefined {
  const m = /^components\/([^/]+)\//.exec(path);
  return m ? m[1] : undefined;
}

/**
 * Build the path for a given component's design.md / openapi.yaml.
 */
export function componentDesignPath(name: string): string {
  return `components/${name}/design.md`;
}

export function componentOpenApiPath(name: string): string {
  return `components/${name}/openapi.yaml`;
}

/**
 * Default content for a freshly created component design.md (frontmatter
 * + minimal body). Used by the "Add component" modal.
 */
export function defaultComponentDesignMd(opts: {
  name: string;
  type: 'service' | 'web-app';
  language: string;
}): string {
  const { name, type, language } = opts;
  const entrypoint =
    type === 'web-app' ? 'deployment/web-application' : 'deployment/service';
  return `---
type: ${type}
language: ${language}
dependsOn: []
buildpack: docker
appPath: /${name}
entrypoint: ${entrypoint}
---

# ${name}

## Overview
TODO: 2–4 sentences describing what this component does.

## Responsibilities
- TODO

## Interfaces
TODO: REST endpoints, events, or user flows. For services, see \`openapi.yaml\`.

## Implementation Notes
TODO: Implementation guidance for the coding-agent.
`;
}

export function defaultComponentOpenApi(name: string): string {
  return `openapi: 3.0.3
info:
  title: ${name}
  version: 0.1.0
paths:
  /health:
    get:
      summary: Liveness probe
      responses:
        '200':
          description: OK
`;
}
