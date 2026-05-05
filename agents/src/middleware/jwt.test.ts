import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import { AddressInfo } from "node:net";
import { generateKeyPair, exportJWK, SignJWT, type JWK, type KeyLike } from "jose";
import express from "express";
import { jwtAuthMiddleware } from "./jwt.js";

interface TestContext {
  jwksServer: Server;
  appServer: Server;
  appPort: number;
  privateKey: KeyLike | Uint8Array;
  jwk: JWK;
  jwksUrl: string;
}

async function listen(server: Server): Promise<number> {
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      resolve((server.address() as AddressInfo).port);
    });
  });
}

async function close(server: Server): Promise<void> {
  return new Promise((resolve) => server.close(() => resolve()));
}

async function setup(): Promise<TestContext> {
  const { publicKey, privateKey } = await generateKeyPair("RS256");
  const jwk = await exportJWK(publicKey);
  jwk.kid = "test-key-1";
  jwk.alg = "RS256";
  jwk.use = "sig";

  const jwksServer = createServer((_req, res) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ keys: [jwk] }));
  });
  const jwksPort = await listen(jwksServer);
  const jwksUrl = `http://127.0.0.1:${jwksPort}/jwks`;

  const app = express();
  app.use(
    jwtAuthMiddleware({
      jwksUrl,
      issuer: "test-iss",
      audience: "test-aud",
    }),
  );
  app.get("/secure", (_req, res) => {
    res.status(200).json({ ok: true });
  });
  const appServer = createServer(app);
  const appPort = await listen(appServer);

  return { jwksServer, appServer, appPort, privateKey, jwk, jwksUrl };
}

async function teardown(ctx: TestContext): Promise<void> {
  await close(ctx.appServer);
  await close(ctx.jwksServer);
}

test("jwtAuthMiddleware: accepts valid token", async () => {
  const ctx = await setup();
  try {
    const token = await new SignJWT({})
      .setProtectedHeader({ alg: "RS256", kid: "test-key-1" })
      .setIssuer("test-iss")
      .setAudience("test-aud")
      .setIssuedAt()
      .setExpirationTime("1h")
      .sign(ctx.privateKey);

    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert.equal(resp.status, 200);
    const body = await resp.json();
    assert.deepEqual(body, { ok: true });
  } finally {
    await teardown(ctx);
  }
});

test("jwtAuthMiddleware: rejects missing Authorization header", async () => {
  const ctx = await setup();
  try {
    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`);
    assert.equal(resp.status, 401);
    const wwwAuth = resp.headers.get("www-authenticate");
    assert.ok(wwwAuth?.startsWith('Bearer realm="asdlc"'), `WWW-Authenticate=${wwwAuth}`);
  } finally {
    await teardown(ctx);
  }
});

test("jwtAuthMiddleware: rejects malformed Authorization header", async () => {
  const ctx = await setup();
  try {
    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`, {
      headers: { Authorization: "NotBearer xyz" },
    });
    assert.equal(resp.status, 401);
    const wwwAuth = resp.headers.get("www-authenticate");
    assert.ok(wwwAuth?.includes('error="invalid_token"'), `WWW-Authenticate=${wwwAuth}`);
  } finally {
    await teardown(ctx);
  }
});

test("jwtAuthMiddleware: rejects wrong audience", async () => {
  const ctx = await setup();
  try {
    const token = await new SignJWT({})
      .setProtectedHeader({ alg: "RS256", kid: "test-key-1" })
      .setIssuer("test-iss")
      .setAudience("wrong-aud")
      .setExpirationTime("1h")
      .sign(ctx.privateKey);

    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert.equal(resp.status, 401);
  } finally {
    await teardown(ctx);
  }
});

test("jwtAuthMiddleware: rejects wrong issuer", async () => {
  const ctx = await setup();
  try {
    const token = await new SignJWT({})
      .setProtectedHeader({ alg: "RS256", kid: "test-key-1" })
      .setIssuer("wrong-iss")
      .setAudience("test-aud")
      .setExpirationTime("1h")
      .sign(ctx.privateKey);

    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert.equal(resp.status, 401);
  } finally {
    await teardown(ctx);
  }
});

test("jwtAuthMiddleware: rejects expired token", async () => {
  const ctx = await setup();
  try {
    // exp in the past
    const token = await new SignJWT({})
      .setProtectedHeader({ alg: "RS256", kid: "test-key-1" })
      .setIssuer("test-iss")
      .setAudience("test-aud")
      .setIssuedAt(Math.floor(Date.now() / 1000) - 7200)
      .setExpirationTime(Math.floor(Date.now() / 1000) - 60)
      .sign(ctx.privateKey);

    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert.equal(resp.status, 401);
  } finally {
    await teardown(ctx);
  }
});

test("jwtAuthMiddleware: rejects unknown kid", async () => {
  const ctx = await setup();
  try {
    // Sign with the right key but lie about kid → JWKS lookup fails.
    const token = await new SignJWT({})
      .setProtectedHeader({ alg: "RS256", kid: "no-such-kid" })
      .setIssuer("test-iss")
      .setAudience("test-aud")
      .setExpirationTime("1h")
      .sign(ctx.privateKey);

    const resp = await fetch(`http://127.0.0.1:${ctx.appPort}/secure`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert.equal(resp.status, 401);
  } finally {
    await teardown(ctx);
  }
});
