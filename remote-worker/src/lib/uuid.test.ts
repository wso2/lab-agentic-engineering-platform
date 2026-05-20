import { test } from "node:test";
import assert from "node:assert/strict";
import { isSlug, isUUID } from "./uuid.js";

test("isUUID: accepts canonical lowercase v4 UUID", () => {
  assert.equal(isUUID("550e8400-e29b-41d4-a716-446655440000"), true);
});

test("isUUID: accepts uppercase UUID", () => {
  assert.equal(isUUID("550E8400-E29B-41D4-A716-446655440000"), true);
});

test("isUUID: rejects empty string", () => {
  assert.equal(isUUID(""), false);
});

test("isUUID: rejects path traversal attempt", () => {
  assert.equal(isUUID("../../etc/passwd"), false);
});

test("isUUID: rejects shell metacharacters", () => {
  assert.equal(isUUID("$(rm -rf /)"), false);
  assert.equal(isUUID("foo;bar"), false);
});

test("isUUID: rejects truncated UUID", () => {
  assert.equal(isUUID("550e8400-e29b-41d4-a716"), false);
});

test("isUUID: rejects extra characters", () => {
  assert.equal(isUUID("550e8400-e29b-41d4-a716-446655440000-extra"), false);
});

test("isUUID: rejects non-string input", () => {
  assert.equal(isUUID(undefined), false);
  assert.equal(isUUID(null), false);
  assert.equal(isUUID(123), false);
  assert.equal(isUUID({}), false);
});

test("isSlug: accepts default org name", () => {
  assert.equal(isSlug("default"), true);
});

test("isSlug: accepts hyphenated project name", () => {
  assert.equal(isSlug("as12111"), true);
  assert.equal(isSlug("hello-world-project"), true);
});

test("isSlug: rejects path traversal", () => {
  assert.equal(isSlug("../etc"), false);
  assert.equal(isSlug("foo/bar"), false);
  assert.equal(isSlug(".hidden"), false);
});

test("isSlug: rejects uppercase", () => {
  assert.equal(isSlug("Default"), false);
});

test("isSlug: rejects too long", () => {
  assert.equal(isSlug("a".repeat(64)), false);
});

test("isSlug: rejects shell metacharacters", () => {
  assert.equal(isSlug("foo;bar"), false);
  assert.equal(isSlug("foo`bar"), false);
});
