import { test } from "node:test";
import assert from "node:assert/strict";
import { Scrubber } from "./scrubber.js";

function fresh(): Scrubber {
  return new Scrubber();
}

test("scrub: passes through unrelated text unchanged", () => {
  const s = fresh();
  s.addLiteral("super-secret-bearer-1234567890");
  const out = s.scrub("services/auth/jwt.go updated");
  assert.equal(out, "services/auth/jwt.go updated");
});

test("scrub: redacts a primed literal", () => {
  const s = fresh();
  s.addLiteral("super-secret-bearer-1234567890");
  const out = s.scrub("Authorization: super-secret-bearer-1234567890 value");
  assert.match(out, /\[REDACTED\]/);
  assert.ok(!out.includes("super-secret-bearer-1234567890"));
});

test("scrub: redacts a primed literal multiple times in one line", () => {
  const s = fresh();
  s.addLiteral("super-secret-bearer-1234567890");
  const out = s.scrub("super-secret-bearer-1234567890 then super-secret-bearer-1234567890 again");
  assert.equal(
    out,
    "[REDACTED] then [REDACTED] again",
  );
});

test("scrub: ignores literals shorter than min length", () => {
  const s = fresh();
  s.addLiteral("short"); // <12 chars — ignored
  const out = s.scrub("the word short appears here");
  assert.equal(out, "the word short appears here");
});

test("scrub: ignores empty / null / undefined literals", () => {
  const s = fresh();
  s.addLiteral("");
  s.addLiteral(null);
  s.addLiteral(undefined);
  const out = s.scrub("nothing should change");
  assert.equal(out, "nothing should change");
});

test("scrub: longer literal wins over substring overlap", () => {
  const s = fresh();
  s.addLiteral("AAAAAAAAAAAA-SHORT");
  s.addLiteral("AAAAAAAAAAAA-SHORT-AND-LONGER");
  const out = s.scrub("token=AAAAAAAAAAAA-SHORT-AND-LONGER end");
  assert.equal(out, "token=[REDACTED] end");
});

test("scrub: redacts ghs_ token", () => {
  const s = fresh();
  const line = "got token ghs_aBCDEFGhijklmnop1234567890 from gh wrapper";
  const out = s.scrub(line);
  assert.match(out, /got token \[REDACTED\] from gh wrapper/);
});

test("scrub: redacts ghp_ and github_pat_ tokens", () => {
  const s = fresh();
  const line = "user PAT ghp_abcdefghijklmnopqrstuv1234 also github_pat_11AAAA_abcdefghijklmnopqrst";
  const out = s.scrub(line);
  assert.ok(!out.includes("ghp_abcdefghijklmnopqrstuv1234"));
  assert.ok(!out.includes("github_pat_11AAAA_abcdefghijklmnopqrst"));
});

test("scrub: preserves Authorization header key, redacts value", () => {
  const s = fresh();
  const line = "curl ... -H \"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature1234567890\" ok";
  const out = s.scrub(line);
  assert.match(out, /Authorization\s*:\s*\[REDACTED\]/i);
  assert.ok(!out.includes("eyJhbGciOiJIUzI1NiJ9.payload.signature1234567890"));
});

test("scrub: redacts x-api-key value", () => {
  const s = fresh();
  const line = "headers: x-api-key: sk-ant-1234567890abcdefghijklmnop ;";
  const out = s.scrub(line);
  assert.match(out, /x-api-key\s*:\s*\[REDACTED\]/i);
});

test("scrub: redacts a high-entropy 32+ char base64 substring", () => {
  const s = fresh();
  const line = "credential=A7Bk39fZqLpNc2RxYwUv0sGmH4dT8jWoEi end";
  const out = s.scrub(line);
  assert.ok(!out.includes("A7Bk39fZqLpNc2RxYwUv0sGmH4dT8jWoEi"));
  assert.match(out, /credential=\[REDACTED\] end/);
});

test("scrub: leaves low-entropy long substring alone (false-positive guard)", () => {
  const s = fresh();
  // 40 'a' chars — long enough to match length but entropy is 0.
  const line = "filler=" + "a".repeat(40) + " end";
  const out = s.scrub(line);
  assert.equal(out, line);
});

test("scrub: leaves moderate-entropy short paths alone", () => {
  const s = fresh();
  const line = "edited services/auth/jwt.go and services/auth/middleware.go";
  const out = s.scrub(line);
  assert.equal(out, line);
});

test("scrub: redacts a JWT-shaped string via entropy backstop", () => {
  const s = fresh();
  const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c";
  const line = `bearer ${jwt} accepted`;
  const out = s.scrub(line);
  // Either the bearer-pattern OR the entropy backstop should catch it.
  assert.ok(!out.includes(jwt));
});

test("scrub: order — literal beats entropy (no double-redact garble)", () => {
  const s = fresh();
  const secret = "X".repeat(40); // would also trigger entropy if not low-entropy
  s.addLiteral(secret);
  const line = `prefix ${secret} suffix`;
  const out = s.scrub(line);
  assert.equal(out, "prefix [REDACTED] suffix");
});

test("scrub: handles multi-line strings", () => {
  const s = fresh();
  s.addLiteral("super-secret-bearer-1234567890");
  const out = s.scrub("line1\nsuper-secret-bearer-1234567890\nline3");
  assert.equal(out, "line1\n[REDACTED]\nline3");
});
