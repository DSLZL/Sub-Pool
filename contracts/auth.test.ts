import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { session } from "./auth";
import { createAuthClient, createUsersClient, observePasswordChangeRequired } from "./client";

const sample = {
  user: { id: "a".repeat(22), username: "admin", role: "admin", revision: "1" },
  created_at: "2026-11-01T01:00:00Z", idle_expires_at: "2026-11-01T09:00:00Z",
  absolute_expires_at: "2026-11-08T01:00:00Z", server_time: "2026-11-01T02:00:00Z",
};

test("auth contract rejects malformed identity, secret additions and invalid deadlines", () => {
  assert.ok(session.safeParse(sample).success);
  assert.ok(session.safeParse({ ...sample, must_change_password: true }).success);
  assert.ok(session.safeParse({ ...sample,
    created_at: "2026-11-01T01:00:00.000000001Z", server_time: "2026-11-01T01:00:00.000000002Z",
    idle_expires_at: "2026-11-01T01:00:00.000000003Z",
  }).success);
  for (const value of [
    { ...sample, token: "secret" }, { ...sample, must_change_password: "true" }, { ...sample, user: { ...sample.user, role: "owner" } },
    { ...sample, user: { ...sample.user, revision: 1 } },
    { ...sample, idle_expires_at: sample.server_time },
    { ...sample, absolute_expires_at: sample.created_at },
    { ...sample, server_time: "2026-02-30T00:00:00Z" },
    { ...sample, created_at: "2026-11-01T09:00:00+08:00" },
  ]) assert.equal(session.safeParse(value).success, false);
});

test("auth client validates responses and never replays a failed mutation", async () => {
  for (const origin of ["https://user:secret@example.com", "https://example.com/", "http://example.com", "https://example.com?x=1"]) {
    assert.throws(() => createAuthClient(origin));
  }
  let calls = 0;
  const client = createAuthClient("https://example.test", async (_request, init) => {
    calls++;
    assert.equal(init?.redirect, "error");
    assert.equal(init?.cache, "no-store");
    throw new TypeError("Connection lost");
  });
  await assert.rejects(client.logout({}));
  assert.equal(calls, 1);
  const invalid = createAuthClient("https://example.test", async () => Response.json({ ...sample, token: "secret" }));
  await assert.rejects(invalid.current({}));
  const denied = createAuthClient("https://example.test", async () => Response.json({
    defined: true, code: "UNAUTHORIZED", status: 401, message: "Rejected", data: { retry_at: null },
  }, { status: 401 }));
  await assert.rejects(denied.current({}), error => error instanceof ORPCError && error.defined && error.status === 401);
});

test("shared client observes only the defined password-change denial without replay", async () => {
  const origins: string[] = [];
  const unsubscribe = observePasswordChangeRequired(origin => origins.push(origin));
  const body = { defined: true, code: "PASSWORD_CHANGE_REQUIRED", status: 403, message: "Rejected", data: { retry_at: null } };
  let calls = 0;
  const client = createUsersClient("https://example.test", async () => {
    calls++;
    return Response.json(calls === 1 ? { ...body, defined: false } : body, { status: 403 });
  });
  try {
    await assert.rejects(client.access({}));
    await assert.rejects(client.access({}));
    assert.equal(calls, 2);
    assert.deepEqual(origins, ["https://example.test"]);
  } finally { unsubscribe(); }
});

test("real Go auth and PostgreSQL session lifecycle through oRPC", {
  skip: !process.env.SUB_POOL_TEST_AUTH_ORIGIN,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_AUTH_ORIGIN;
  const username = process.env.SUB_POOL_TEST_AUTH_USERNAME;
  const password = process.env.SUB_POOL_TEST_AUTH_PASSWORD;
  assert.ok(origin && username && password, "explicit isolated auth fixture credentials required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  let cookie = "";
  const client = createAuthClient(origin, async (input, init) => {
    const request = new Request(input, init);
    request.headers.set("Origin", origin);
    if (cookie) request.headers.set("Cookie", cookie);
    const response = await fetch(request, { redirect: "error" });
    const issued = response.headers.get("set-cookie");
    if (issued) cookie = issued.split(";")[0] ?? "";
    return response;
  });
  await assert.rejects(client.current({}), error => error instanceof ORPCError && error.status === 401);
  const first = await client.login({ username, password });
  assert.equal(first.user.role, "admin");
  const restored = await client.current({});
  assert.equal(restored.user.id, first.user.id);
  assert.equal(restored.idle_expires_at, first.idle_expires_at);
  if (first.must_change_password) {
    assert.equal(restored.must_change_password, true);
    await assert.rejects(client.renew({}), error => error instanceof ORPCError && error.defined
      && error.status === 403 && error.code === "PASSWORD_CHANGE_REQUIRED");
  } else {
    const renewed = await client.renew({});
    assert.equal(renewed.absolute_expires_at, first.absolute_expires_at);
  }
  assert.equal((await client.logout({})).logged_out, true);
  await assert.rejects(client.current({}), error => error instanceof ORPCError && error.status === 401);
  await client.login({ username, password });
  const next = password + "Changed2";
  assert.deepEqual(await client.changePassword({ current_password: password, new_password: next }), { password_changed: true, sessions_revoked: true });
  await assert.rejects(client.current({}), error => error instanceof ORPCError && error.status === 401);
  await assert.rejects(client.login({ username, password }), error => error instanceof ORPCError && error.status === 401);
  const changed = await client.login({ username, password: next });
  assert.equal(changed.user.revision, "2");
  assert.equal(changed.must_change_password, undefined);
  await client.logout({});
});
