// RFC 4122 UUID, any version. Strict — case insensitive but no surrounding
// braces, no truncation, no other characters allowed.
const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUUID(s: unknown): s is string {
  return typeof s === "string" && UUID_RE.test(s);
}

// DNS-label-shaped slug (lowercase). Matches what git-service uses for
// ocOrgID validation and what the BFF generates for project names.
// Rejects path traversal (`..`, `/`, leading dots), shell metacharacters,
// uppercase, and overlong values. ≤ 63 chars per DNS label.
const SLUG_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;

export function isSlug(s: unknown): s is string {
  return typeof s === "string" && SLUG_RE.test(s);
}
