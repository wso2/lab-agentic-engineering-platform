// OAuth2 client_credentials token helper. Mirrors the canonical Go shape
// at wso2cloud/backend/core/pkg/thunder/auth/token_provider.go: cache
// the token, refresh on a 5-minute renewal buffer, honour `expires_in`,
// HTTP Basic auth (client_secret_basic).
//
// WS2.4 runner-auth: the runner pod gets per-org Thunder publisher cc
// credentials via per-run ExternalSecret (PUBLISHER_CLIENT_ID +
// PUBLISHER_CLIENT_SECRET) and the token URL via plain env
// (PUBLISHER_TOKEN_URL). This helper mints + caches access tokens that
// authenticate the runner's callbacks to asdlc-api.

import http from "node:http";
import https from "node:https";
import { URL } from "node:url";

export interface ClientCredentialsConfig {
  tokenUrl: string;
  clientId: string;
  clientSecret: string;
  // renewalBufferMs — how long before expiry to refresh. Default 5 min.
  renewalBufferMs?: number;
  // fallbackTtlMs — token lifetime when the server omits expires_in.
  // Default 50 min (matches the upstream Go helper's fallback).
  fallbackTtlMs?: number;
}

const DEFAULT_RENEWAL_BUFFER_MS = 5 * 60 * 1000;
const DEFAULT_FALLBACK_TTL_MS = 50 * 60 * 1000;

export class ClientCredentialsTokenProvider {
  private readonly tokenUrl: URL;
  private readonly basicAuth: string;
  private readonly renewalBufferMs: number;
  private readonly fallbackTtlMs: number;

  private cachedToken: string | undefined;
  private expiresAtMs = 0;
  private inflight: Promise<string> | undefined;

  constructor(config: ClientCredentialsConfig) {
    if (!config.tokenUrl) throw new Error("ClientCredentialsTokenProvider: tokenUrl required");
    if (!config.clientId) throw new Error("ClientCredentialsTokenProvider: clientId required");
    if (!config.clientSecret) throw new Error("ClientCredentialsTokenProvider: clientSecret required");
    this.tokenUrl = new URL(config.tokenUrl);
    this.basicAuth =
      "Basic " +
      Buffer.from(`${config.clientId}:${config.clientSecret}`, "utf8").toString("base64");
    this.renewalBufferMs = config.renewalBufferMs ?? DEFAULT_RENEWAL_BUFFER_MS;
    this.fallbackTtlMs = config.fallbackTtlMs ?? DEFAULT_FALLBACK_TTL_MS;
  }

  async getToken(): Promise<string> {
    if (this.cachedToken && Date.now() < this.expiresAtMs - this.renewalBufferMs) {
      return this.cachedToken;
    }
    if (this.inflight) {
      return this.inflight;
    }
    this.inflight = this.fetchToken().finally(() => {
      this.inflight = undefined;
    });
    return this.inflight;
  }

  private fetchToken(): Promise<string> {
    return new Promise((resolve, reject) => {
      const body = "grant_type=client_credentials";
      const lib = this.tokenUrl.protocol === "https:" ? https : http;
      const req = lib.request(
        this.tokenUrl,
        {
          method: "POST",
          headers: {
            Authorization: this.basicAuth,
            "Content-Type": "application/x-www-form-urlencoded",
            "Content-Length": Buffer.byteLength(body).toString(),
            Accept: "application/json",
          },
          timeout: 10000,
        },
        (res) => {
          let chunks = "";
          res.on("data", (c: Buffer) => {
            chunks += c.toString();
          });
          res.on("end", () => {
            if ((res.statusCode ?? 0) < 200 || (res.statusCode ?? 0) >= 300) {
              return reject(
                new Error(
                  `cc token endpoint returned ${res.statusCode}: ${chunks.slice(0, 200)}`,
                ),
              );
            }
            try {
              const parsed = JSON.parse(chunks);
              const accessToken: unknown = parsed?.access_token;
              if (typeof accessToken !== "string" || accessToken === "") {
                return reject(new Error("cc token response missing access_token"));
              }
              const expiresIn: unknown = parsed?.expires_in;
              const ttlMs =
                typeof expiresIn === "number" && expiresIn > 0
                  ? expiresIn * 1000
                  : this.fallbackTtlMs;
              this.cachedToken = accessToken;
              this.expiresAtMs = Date.now() + ttlMs;
              resolve(accessToken);
            } catch (err) {
              reject(
                new Error(
                  `invalid cc token response: ${err instanceof Error ? err.message : String(err)}`,
                ),
              );
            }
          });
        },
      );
      req.on("error", (err) => reject(err));
      req.on("timeout", () => {
        req.destroy();
        reject(new Error("cc token request timed out"));
      });
      req.write(body);
      req.end();
    });
  }
}
