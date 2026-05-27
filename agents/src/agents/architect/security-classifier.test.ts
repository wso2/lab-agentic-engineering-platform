import { test } from "node:test";
import assert from "node:assert/strict";
import {
  classifyAPISecurity,
  matchingFamilies,
} from "./security-classifier.js";

// Golden tests for the `exposesAPI` rubric from prompt.ts. These are the
// canonical examples the architect agent is expected to handle the same
// way. When the rubric changes, update these tests AND the prompt — they
// must move together. Five cases cover one path each:
//   1. public hello-world (no triggers)
//   2. login surface (auth-verbs)
//   3. JWT-protected API (identity-tokens)
//   4. billing service (payment-regulated)
//   5. customer-data store (user-scoped-data) — also confirms 'private'
//      from access-intent stacks correctly when multiple families hit.

test("hello-world description classifies as none", () => {
  const desc =
    "A minimal Go HTTP service exposing GET /hello returning a JSON Hello World response on port 9090. No persistence, no users, no external dependencies.";
  assert.equal(classifyAPISecurity(desc), "none");
  assert.deepEqual(matchingFamilies(desc), []);
});

test("login endpoint description classifies as required (auth-verbs)", () => {
  const desc =
    "Implement a /auth/login endpoint that accepts username + password and returns a session token. Stores hashed credentials in embedded SQLite.";
  assert.equal(classifyAPISecurity(desc), "required");
  assert.ok(matchingFamilies(desc).includes("auth-verbs"));
});

test("JWT-validating API classifies as required (identity-tokens)", () => {
  const desc =
    "Expose /todos endpoints that validate the caller's JWT bearer token issued by the auth service. The service trusts cluster-issued JWTs only.";
  assert.equal(classifyAPISecurity(desc), "required");
  assert.ok(matchingFamilies(desc).includes("identity-tokens"));
});

test("billing service classifies as required (payment-regulated)", () => {
  const desc =
    "Stripe-backed billing service. Receives subscription webhooks and stores invoice records in SQLite. Issues monthly billing summaries.";
  assert.equal(classifyAPISecurity(desc), "required");
  assert.ok(matchingFamilies(desc).includes("payment-regulated"));
});

test("customer-data store stacks user-scoped-data + access-intent", () => {
  const desc =
    "Private CRM API that stores customer contact records (name, email, phone). All endpoints require an authorized caller.";
  assert.equal(classifyAPISecurity(desc), "required");
  const fams = matchingFamilies(desc);
  assert.ok(fams.includes("user-scoped-data"));
  assert.ok(fams.includes("access-intent"));
});

// Defensive edge cases — not in the canonical 5 but worth pinning so the
// rubric doesn't drift toward false positives.

test("substring inside larger words does not trigger (word-boundary)", () => {
  // 'controller' contains the substring 'roll' — close to 'role' but not
  // matching. Make sure no keyword family fires when the only matches
  // are non-word-boundary substrings.
  const desc =
    "Background controller that polls upstream feeds and writes to a local cache. Purely public.";
  assert.equal(
    classifyAPISecurity(desc),
    "none",
    "word-boundary regex must reject 'controller' and similar substrings",
  );
});

test("empty input classifies as none", () => {
  assert.equal(classifyAPISecurity(""), "none");
  assert.deepEqual(matchingFamilies(""), []);
});
