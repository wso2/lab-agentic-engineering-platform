// Registry of document types supported in the requirements directory.
// Each type defines its default filename, whether it's unique, whether
// it's protected from delete/rename, and (optionally) which agents-service
// skill generates it from sibling files.

export type DocumentTypeId =
  | 'requirements'
  | 'functional-requirements'
  | 'non-functional-requirements'
  | 'user-stories'
  | 'wireframes'
  | 'domain-model';

export interface DocumentType {
  id: DocumentTypeId;
  label: string;
  description: string;
  defaultFilename: string;
  extension: string;
  unique: boolean;
  protected?: boolean;
  /** ID of an agents-service document-generation skill, if any. */
  generationSkillId?: string;
  /** Source documents (by filename) the skill needs as context. */
  generationSourceFiles?: string[];
}

export const DOCUMENT_TYPES: DocumentType[] = [
  {
    id: 'requirements',
    label: 'Requirements',
    description: 'High-level product sketch — Overview / Personas / Features.',
    defaultFilename: 'requirements.md',
    extension: '.md',
    unique: true,
    protected: true,
    generationSkillId: 'requirements-from-prompt',
  },
  {
    id: 'functional-requirements',
    label: 'Functional Requirements',
    description: 'EARS-style functional requirements derived from the main requirements.',
    defaultFilename: 'functional-requirements.md',
    extension: '.md',
    unique: false,
    generationSkillId: 'functional-requirements',
    generationSourceFiles: ['requirements.md'],
  },
  {
    id: 'non-functional-requirements',
    label: 'Non-Functional Requirements',
    description: 'Quality attributes — performance, security, availability, etc.',
    defaultFilename: 'non-functional-requirements.md',
    extension: '.md',
    unique: false,
    generationSkillId: 'non-functional-requirements',
    generationSourceFiles: ['requirements.md'],
  },
  {
    id: 'user-stories',
    label: 'User Stories',
    description: 'User-perspective stories with acceptance criteria.',
    defaultFilename: 'user-stories.md',
    extension: '.md',
    unique: false,
    generationSkillId: 'user-stories',
    generationSourceFiles: ['requirements.md', 'functional-requirements.md'],
  },
  {
    id: 'wireframes',
    label: 'Wireframes',
    description: 'UI sketches and screen flows (Excalidraw canvas).',
    defaultFilename: 'wireframes.excalidraw',
    extension: '.excalidraw',
    unique: false,
    generationSkillId: 'wireframes',
    generationSourceFiles: [
      'requirements.md',
      'functional-requirements.md',
      'user-stories.md',
    ],
  },
  {
    id: 'domain-model',
    label: 'Domain Model',
    description: 'Entities, relationships, and domain concepts (Excalidraw canvas).',
    defaultFilename: 'domain-model.excalidraw',
    extension: '.excalidraw',
    unique: false,
    generationSkillId: 'domain-model',
    generationSourceFiles: [
      'requirements.md',
      'functional-requirements.md',
      'user-stories.md',
    ],
  },
];

const DOCUMENT_TYPES_BY_ID = new Map<DocumentTypeId, DocumentType>(
  DOCUMENT_TYPES.map((t) => [t.id, t]),
);

export function getDocumentType(id: DocumentTypeId | string): DocumentType | undefined {
  return DOCUMENT_TYPES_BY_ID.get(id as DocumentTypeId);
}

/**
 * Match a filename to its document type. Falls back to undefined for
 * filenames that don't correspond to a registered type (user-renamed docs).
 *
 * Multiple types may share an extension (e.g. `wireframes` and `domain-model`
 * are both `.excalidraw`); the stem prefix disambiguates them.
 */
export function documentTypeForFile(filename: string): DocumentType | undefined {
  const lower = filename.toLowerCase();
  return DOCUMENT_TYPES.find((t) => {
    const ext = t.extension.toLowerCase();
    if (lower === t.defaultFilename.toLowerCase()) return true;
    if (!t.unique) {
      const stem = t.defaultFilename.slice(0, -ext.length).toLowerCase();
      if (lower.startsWith(stem) && lower.endsWith(ext)) return true;
    }
    return false;
  });
}

const EXT_RE = /\.(md|markdown|excalidraw|dsl)$/i;

/**
 * Title-case a filename (strip extension, split on `-`/space, capitalise
 * each token). Used as the sidebar fallback when a file doesn't match a
 * registered document type — e.g. user-renamed `my-doc.md` → "My Doc".
 */
export function toTitleCase(name: string): string {
  return name
    .replace(EXT_RE, '')
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ');
}

/**
 * Compute the next free filename for a given doc type. Unique types return
 * the canonical filename; non-unique types append `-2`, `-3`, ... when the
 * default already exists.
 */
export function nextFilenameFor(type: DocumentType, existing: string[]): string {
  const existingSet = new Set(existing.map((n) => n.toLowerCase()));
  if (type.unique) {
    return type.defaultFilename;
  }
  const ext = type.extension;
  const stem = type.defaultFilename.slice(0, -ext.length);
  if (!existingSet.has(type.defaultFilename.toLowerCase())) {
    return type.defaultFilename;
  }
  let n = 2;
  while (existingSet.has(`${stem}-${n}${ext}`.toLowerCase())) n++;
  return `${stem}-${n}${ext}`;
}
