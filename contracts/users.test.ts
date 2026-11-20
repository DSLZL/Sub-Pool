import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { access, grants, managedUser } from "./users";
import { createAuthClient, createUsersClient } from "./client";

const id = "a".repeat(21) + "A";
const sample = {
  id, username: "reader", role: "user", revision: "1", enabled: true,
  created_at: "2026-11-01T01:00:00Z", features: ["usage"], account_ids: [id], participant_ids: [],
  feature_count: 1, account_count: 1, participant_count: 0, last_login_at: null,
};
test("users schemas validate role, exclude secrets and reject ambiguous scopes", () => {
  assert.ok(managedUser.safeParse(sample).success);
  assert.ok(managedUser.safeParse({ ...sample, role: "admin" }).success);
  for (const value of [
    { ...sample, role: "owner" }, { ...sample, password_hash: "secret" },
    { ...sample, current_password: "secret" },
    { ...sample, features: ["users"] }, { ...sample, account_ids: [id, id] },
    { ...sample, account_ids: null }, { ...sample, participant_ids: ["a".repeat(22)] },
    { ...sample, revision: "9223372036854775808" },
  ]) assert.equal(managedUser.safeParse(value).success, false);
  assert.equal(grants.safeParse({ features: [], account_ids: [], participant_ids: [] }).success, true);
  assert.equal(access.safeParse({ user: { id, username: "reader", role: "user", revision: "1" },
    all_data: true, features: [], account_ids: [], participant_ids: [] }).success, false);
});

test("users client validates inputs/results, preserves conflicts and never retries writes", async () => {
  let calls = 0;
  const client = createUsersClient("https://example.test", async () => {
    calls++;
    throw new TypeError("Connection lost after dispatch");
  });
  await assert.rejects(client.create({ username: "reader", password: "NewPassword123", enabled: true,
    features: [], account_ids: [], participant_ids: [] }));
  assert.equal(calls, 1);
  await assert.rejects(client.update({ ...sample, enabled: true, role: "admin" } as never));
  assert.equal(calls, 1);
  const invalid = createUsersClient("https://example.test", async () => Response.json({ ...sample, password: "secret" }));
  await assert.rejects(invalid.get({ id }));
  const conflict = createUsersClient("https://example.test", async () => Response.json({
    defined: true, code: "CONFLICT", status: 409, message: "Request could not be completed.", data: { retry_at: null },
  }, { status: 409 }));
  await assert.rejects(conflict.update({ id, revision: "1", username: "reader", enabled: true,
    features: [], account_ids: [], participant_ids: [], new_password: "" }), error => error instanceof ORPCError && error.defined && error.status === 409);
});

test("real Go/PostgreSQL users and permissions through oRPC", {
  skip: !process.env.SUB_POOL_TEST_USERS_ORIGIN,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_USERS_ORIGIN;
  const username = process.env.SUB_POOL_TEST_USERS_USERNAME;
  const password = process.env.SUB_POOL_TEST_USERS_PASSWORD;
  assert.ok(origin && username && password, "explicit isolated user-management fixture required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      request.headers.set("Origin", origin!);
      if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" });
      const issued = response.headers.get("set-cookie");
      if (issued) cookie = issued.split(";")[0] ?? "";
      return response;
    };
    return { auth: createAuthClient(origin!, fetcher), users: createUsersClient(origin!, fetcher) };
  }
  const admin = browser(), member = browser();
  await admin.auth.login({ username, password });
  assert.equal((await admin.users.access({})).all_data, true);
  let user = await admin.users.create({ username: "reader." + crypto.randomUUID().slice(0, 12), password: "OriginalPassword123",
    enabled: true, features: [], account_ids: [], participant_ids: [] });
  assert.equal(user.role, "user");
  await member.auth.login({ username: user.username, password: "OriginalPassword123" });
  assert.equal((await member.users.access({})).all_data, false);
  await assert.rejects(member.users.get({ id: user.id }), error => error instanceof ORPCError && error.status === 403);
  const input = { id: user.id, revision: user.revision, username: user.username, enabled: true,
    features: ["usage"] as const, account_ids: [id], participant_ids: [] as string[], new_password: "" };
  user = await admin.users.update({ ...input, features: [...input.features] });
  assert.equal(user.revision, "2");
  assert.deepEqual((await member.users.access({})).account_ids, [id]);
  await assert.rejects(admin.users.update({ ...input, features: [...input.features] }), error => error instanceof ORPCError && error.status === 409);
  assert.equal((await admin.users.list({ after: "", search: user.username, limit: 10 })).users[0]?.id, user.id);
  assert.equal((await admin.users.get({ id: user.id })).revision, user.revision);
  const promotion = { ...input, revision: user.revision, features: [...input.features], role: "admin" as const };
  await assert.rejects(admin.users.update(promotion), error => error instanceof ORPCError &&
    error.defined && error.code === "CURRENT_PASSWORD_INVALID" && error.status === 403);
  user = await admin.users.update({ ...promotion, current_password: password });
  assert.equal(user.role, "admin");
  await assert.rejects(member.auth.current({}), error => error instanceof ORPCError && error.status === 401);
  assert.equal((await admin.users.get({ id: user.id, role: "admin" })).role, "admin");
  await member.auth.login({ username: user.username, password: "OriginalPassword123" });
  assert.equal((await member.users.access({})).all_data, true);
  user = await admin.users.update({ ...promotion, revision: user.revision, role: "user", current_password: password });
  assert.equal(user.role, "user");
  await assert.rejects(member.auth.current({}), error => error instanceof ORPCError && error.status === 401);
  await member.auth.login({ username: user.username, password: "OriginalPassword123" });
  user = await admin.users.update({ id: user.id, revision: user.revision, username: user.username,
    enabled: true, features: user.features, account_ids: user.account_ids, participant_ids: user.participant_ids,
    new_password: "ReplacementPassword234" });
  await assert.rejects(member.users.access({}), error => error instanceof ORPCError && error.status === 401);
  await member.auth.login({ username: user.username, password: "ReplacementPassword234" });
  user = await admin.users.get({ id: user.id });
  user = await admin.users.update({ id: user.id, revision: user.revision, username: user.username,
    enabled: false, features: user.features, account_ids: user.account_ids, participant_ids: user.participant_ids, new_password: "" });
  await assert.rejects(member.auth.current({}), error => error instanceof ORPCError && error.status === 401);
  assert.deepEqual(await admin.users.delete({ id: user.id, revision: user.revision, confirmation: user.username }), { deleted: true });
  await assert.rejects(admin.users.get({ id: user.id }), error => error instanceof ORPCError && error.status === 404);
  await admin.auth.logout({});
});
