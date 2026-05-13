/**
 * Anthropic key resolver.
 *
 * Per-request resolution of the effective Anthropic API key for a given OC
 * org. Resolves via git-service's
 * GET /internal/credentials/orgs/{orgId}/anthropic/effective-key endpoint,
 * which returns `{source:"org"|"platform"|"none", key:"sk-ant-..."}`. The
 * key bytes are cached in-process in a 5-minute LRU keyed by orgId; the
 * invalidate route at POST /v1/internal/cache/invalidate drops one entry
 * eagerly on Connect/Disconnect events from git-service.
 *
 * See docs/design/anthropic-key-dual-token.md §6.4.
 */

export type EffectiveKeySource = "org" | "platform" | "none";

export interface EffectiveKey {
  source: EffectiveKeySource;
  key: string; // empty when source === "none"
}

export class AnthropicKeyError extends Error {
  constructor(
    message: string,
    public readonly code:
      | "no_anthropic_key_configured"
      | "resolver_unreachable"
      | "resolver_error",
    public readonly status: number = 502,
  ) {
    super(message);
    this.name = "AnthropicKeyError";
  }
}

const CACHE_TTL_MS = 5 * 60 * 1000;

interface CacheEntry {
  value: EffectiveKey;
  expiresAt: number; // ms-since-epoch
}

const cache = new Map<string, CacheEntry>();

/**
 * Invalidate the cached entry for ocOrgId. Called by the
 * /v1/internal/cache/invalidate route on Connect/Disconnect.
 */
export function invalidateAnthropicCache(ocOrgId: string): void {
  cache.delete(ocOrgId);
}

/**
 * Reset the entire cache. Useful in tests.
 */
export function resetAnthropicCache(): void {
  cache.clear();
}

function gitServiceUrl(): string {
  const url =
    process.env.GIT_SERVICE_URL ||
    process.env.ASDLC_GIT_SERVICE_URL ||
    "http://app-factory-git-service:3300";
  return url.replace(/\/+$/, "");
}

/**
 * Resolve the effective Anthropic key for ocOrgId. Throws AnthropicKeyError
 * when no key is configured (org row absent AND platform env empty) or
 * when the resolver itself is unreachable / returns a non-2xx.
 *
 * The returned key is used inline by `createAnthropic({ apiKey: key })`
 * — see shared/create-agent.ts. It is never logged.
 */
export async function resolveAnthropicKey(
  ocOrgId: string,
): Promise<EffectiveKey> {
  if (!ocOrgId) {
    throw new AnthropicKeyError(
      "orgId is required to resolve an Anthropic API key",
      "resolver_error",
      400,
    );
  }

  const now = Date.now();
  const cached = cache.get(ocOrgId);
  if (cached && cached.expiresAt > now) {
    return cached.value;
  }

  let resp: Response;
  try {
    resp = await fetch(
      `${gitServiceUrl()}/internal/credentials/orgs/${encodeURIComponent(ocOrgId)}/anthropic/effective-key`,
      {
        method: "GET",
        headers: {
          accept: "application/json",
        },
      },
    );
  } catch (err) {
    throw new AnthropicKeyError(
      `git-service unreachable: ${(err as Error).message}`,
      "resolver_unreachable",
      502,
    );
  }

  if (!resp.ok) {
    const body = await resp.text().catch(() => "");
    throw new AnthropicKeyError(
      `git-service returned ${resp.status}: ${body.slice(0, 200)}`,
      "resolver_error",
      502,
    );
  }

  const data = (await resp.json()) as EffectiveKey;
  if (!data || (data.source !== "org" && data.source !== "platform" && data.source !== "none")) {
    throw new AnthropicKeyError(
      "git-service returned an unexpected effective-key shape",
      "resolver_error",
      502,
    );
  }

  if (data.source === "none" || !data.key) {
    // No org key + no platform fallback. Don't cache the absence — the user
    // may configure a key seconds from now and we want the next call to
    // pick it up without waiting for TTL.
    throw new AnthropicKeyError(
      "no Anthropic API key configured for this organization (and no platform fallback)",
      "no_anthropic_key_configured",
      503,
    );
  }

  cache.set(ocOrgId, {
    value: data,
    expiresAt: now + CACHE_TTL_MS,
  });

  console.log(
    `[anthropic-key-resolver] orgId=${ocOrgId} source=${data.source}`,
  );

  return data;
}
