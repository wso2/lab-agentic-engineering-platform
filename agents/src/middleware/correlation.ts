import type { Request, Response, NextFunction, RequestHandler } from "express";
import { randomUUID } from "node:crypto";

export const CORRELATION_HEADER = "x-correlation-id";

/**
 * Reads X-Correlation-ID from the request, generating a UUID if missing.
 * Echoes it back on the response and attaches to res.locals.correlationId
 * so handlers and outbound clients can pick it up.
 */
export function correlationIdMiddleware(): RequestHandler {
  return function correlation(req: Request, res: Response, next: NextFunction): void {
    let id = req.header(CORRELATION_HEADER);
    if (!id || id.length === 0 || id.length > 128) {
      id = randomUUID();
    }
    res.setHeader(CORRELATION_HEADER, id);
    (res.locals as Record<string, unknown>).correlationId = id;
    next();
  };
}

/** Convenience getter — returns the correlation ID for the current request. */
export function getCorrelationId(res: Response): string {
  const v = (res.locals as Record<string, unknown>).correlationId;
  return typeof v === "string" ? v : "-";
}
