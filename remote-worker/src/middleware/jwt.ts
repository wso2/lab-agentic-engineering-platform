import type { Request, Response, NextFunction, RequestHandler } from "express";
import { createRemoteJWKSet, jwtVerify, errors as joseErrors } from "jose";

export interface JWTAuthOptions {
  /** Thunder/IDP JWKS endpoint. */
  jwksUrl: string;
  /** Acceptable iss claim value. */
  issuer: string | string[];
  /** Acceptable aud claim value. */
  audience: string;
  /**
   * RFC 9728 protected-resource metadata URL. Included in the
   * WWW-Authenticate Bearer challenge when set.
   */
  resourceMetadataUrl?: string;
}

const REALM = "asdlc";

function buildBearerChallenge(resourceMetadataUrl: string | undefined, errorCode: string): string {
  const parts: string[] = [`realm="${REALM}"`];
  if (errorCode) parts.push(`error="${errorCode}"`);
  if (resourceMetadataUrl) parts.push(`resource_metadata="${resourceMetadataUrl}"`);
  return `Bearer ${parts.join(", ")}`;
}

/**
 * Express middleware that validates a JWT against a remote JWKS.
 *
 * Uses jose.createRemoteJWKSet, which has a built-in cache and coalesces
 * concurrent fetches. On a kid miss it triggers one refresh before failing —
 * matches the behaviour of the Go middleware.
 *
 * Verified claims are attached to res.locals.tokenClaims; the raw token to
 * res.locals.token. Both fields are typed via the express namespace
 * augmentation in src/types/express.d.ts (or the consuming service's
 * equivalent) — but kept loose here to avoid a hard module dependency.
 */
export function jwtAuthMiddleware(opts: JWTAuthOptions): RequestHandler {
  const jwks = createRemoteJWKSet(new URL(opts.jwksUrl));

  return async function jwtAuth(req: Request, res: Response, next: NextFunction): Promise<void> {
    const auth = req.header("Authorization");
    if (!auth) {
      res.setHeader("WWW-Authenticate", buildBearerChallenge(opts.resourceMetadataUrl, ""));
      res.status(401).json({ error: "missing Authorization header" });
      return;
    }
    const m = /^Bearer\s+(.+)$/i.exec(auth);
    if (!m) {
      res.setHeader("WWW-Authenticate", buildBearerChallenge(opts.resourceMetadataUrl, "invalid_token"));
      res.status(401).json({ error: "malformed Authorization header" });
      return;
    }
    const token = m[1].trim();

    try {
      const { payload, protectedHeader } = await jwtVerify(token, jwks, {
        issuer: opts.issuer,
        audience: opts.audience,
        algorithms: ["RS256"],
      });
      // jose.jwtVerify already validates iss, aud, exp, nbf when the options
      // are passed. We only need to surface the verified claims downstream.
      (res.locals as Record<string, unknown>).tokenClaims = payload;
      (res.locals as Record<string, unknown>).tokenHeader = protectedHeader;
      (res.locals as Record<string, unknown>).token = token;
      next();
    } catch (err) {
      const code = err instanceof joseErrors.JOSEError ? err.code : "invalid_token";
      const msg = err instanceof Error ? err.message : "invalid jwt";
      // eslint-disable-next-line no-console
      console.warn("jwt validation failed", { code, msg });
      res.setHeader("WWW-Authenticate", buildBearerChallenge(opts.resourceMetadataUrl, "invalid_token"));
      res.status(401).json({ error: "invalid jwt" });
    }
  };
}
