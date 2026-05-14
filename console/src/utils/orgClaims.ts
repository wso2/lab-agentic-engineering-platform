import type { UserClaims } from '../auth/useUserClaims';

/**
 * Resolve the canonical OC org handle from a verified JWT, preferring
 * `ouHandle` over `ouName` over `ouId`. Returns `undefined` when the
 * token has none of those claims — callers MUST surface this as a
 * fail-loud error rather than silently substitute an org.
 *
 * The BFF mirrors this precedence verbatim
 * (asdlc-service/middleware/jwt.ResolveOuHandle). Any change here MUST
 * land on both sides simultaneously.
 */
export function resolveOuHandle(claims: UserClaims | null | undefined): string | undefined {
  if (!claims) return undefined;
  return claims.ouHandle || claims.ouName || claims.ouId || undefined;
}
