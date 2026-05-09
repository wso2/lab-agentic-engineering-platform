// Registry of document types supported in the requirements directory.
// Each type defines its default filename, whether it's unique, whether
// it's protected from delete/rename, and (optionally) which agents-service
// skill generates it from sibling files.

export type DocumentTypeId =
  | 'requirements'
  | 'functional-requirements'
  | 'non-functional-requirements'
  | 'user-stories';

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
 */
export function documentTypeForFile(filename: string): DocumentType | undefined {
  const lower = filename.toLowerCase();
  return DOCUMENT_TYPES.find((t) => {
    // Exact match (e.g. "requirements.md") OR derived match
    // (e.g. "functional-requirements-2.md" maps to functional-requirements).
    if (lower === t.defaultFilename.toLowerCase()) return true;
    if (!t.unique) {
      const stem = t.defaultFilename.replace(/\.md$/i, '').toLowerCase();
      if (lower.startsWith(stem) && lower.endsWith('.md')) return true;
    }
    return false;
  });
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
  const stem = type.defaultFilename.replace(/\.md$/i, '');
  if (!existingSet.has(type.defaultFilename.toLowerCase())) {
    return type.defaultFilename;
  }
  let n = 2;
  while (existingSet.has(`${stem}-${n}.md`.toLowerCase())) n++;
  return `${stem}-${n}.md`;
}
