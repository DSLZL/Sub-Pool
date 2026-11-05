import assert from "node:assert/strict";
import { test } from "node:test";
import { createAuthClient } from "@sub-pool/contracts/client";
import { SessionController } from "./session";

function value(role: "admin" | "user" = "admin", revision = "1", idleMinutes = 480) {
  const time = Date.UTC(2026, 10, 6);
  return {
    user: { id: "A".repeat(22), username: "admin", role, revision },
    created_at: new Date(time).toISOString(), server_time: new Date(time + 1000).toISOString(),
    idle_expires_at: new Date(time + idleMinutes * 60_000).toISOString(),
    absolute_expires_at: new Date(time + 7 * 86_400_000).toISOString(),
  };
}
function denied(status = 401) {
  return Response.json({ defined: true, code: status === 400 ? "BAD_REQUEST" : "UNAUTHORIZED", status,
    message: "SECRET_SENTINEL", data: { retry_at: null } }, { status });
}

test("unresolved/failed restore never creates an administrator or synthetic session", async () => {
  const api = createAuthClient("https://pool.test", async () => { throw new TypeError("SECRET_SENTINEL"); });
  const controller = new SessionController(() => api);
  try {
    assert.equal(controller.getSnapshot().status, "loading");
    assert.equal(controller.getSnapshot().session, null);
    await controller.refresh();
    assert.equal(controller.getSnapshot().status, "unavailable");
    assert.equal(controller.getSnapshot().session, null);
    assert.ok(!JSON.stringify(controller.getSnapshot()).includes("SECRET_SENTINEL"));
  } finally { controller.dispose(); }
});

test("late restored identity cannot undo an explicit logout", async () => {
  const delayed = Promise.withResolvers<Response>();
  const started = Promise.withResolvers<void>();
  const api = createAuthClient("https://pool.test", async input => {
    if (new URL(input instanceof Request ? input.url : String(input)).pathname.endsWith("/session")) {
      started.resolve(); return delayed.promise;
    }
    return Response.json({ logged_out: true });
  });
  const controller = new SessionController(() => api);
  try {
    const restore = controller.refresh(); await started.promise;
    assert.equal(await controller.logout(), true);
    delayed.resolve(Response.json(value())); await restore;
    assert.equal(controller.getSnapshot().status, "anonymous");
    assert.equal(controller.getSnapshot().reason, "logout");
    assert.equal(controller.getSnapshot().session, null);
  } finally { controller.dispose(); }
});

test("double submission is suppressed and passwords never enter retained session state", async () => {
  const delayed = Promise.withResolvers<Response>();
  let dispatched = 0;
  const api = createAuthClient("https://pool.test", async () => { dispatched++; return delayed.promise; });
  const controller = new SessionController(() => api);
  try {
    const login = controller.login("admin", "ChosenPassword123");
    assert.equal(await controller.login("admin", "ChosenPassword123"), false);
    delayed.resolve(Response.json(value())); assert.equal(await login, true);
    assert.equal(dispatched, 1);
    assert.ok(!JSON.stringify(controller.getSnapshot()).includes("ChosenPassword123"));
  } finally { controller.dispose(); }
});

test("password rejection preserves identity; an uncertain dispatched change clears it without replay", async () => {
  let mode = "proof", changes = 0;
  const api = createAuthClient("https://pool.test", async input => {
    const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
    if (path.endsWith("/password")) {
      changes++;
      if (mode === "proof") return denied(400);
      throw new TypeError("Connection lost after dispatch");
    }
    if (mode === "readerror") throw new TypeError("Read failed");
    return mode === "revoked" ? denied() : Response.json(value());
  });
  const controller = new SessionController(() => api);
  try {
    await controller.refresh();
    assert.equal(await controller.changePassword("WrongPassword123", "ReplacementPassword456"), false);
    assert.equal(controller.getSnapshot().status, "authenticated");
    assert.ok(!controller.getSnapshot().message?.includes("SECRET_SENTINEL"));
    const proofMessage = controller.getSnapshot().message;
    await controller.refresh();
    assert.equal(controller.getSnapshot().message, proofMessage);
    mode = "uncertain";
    assert.equal(await controller.changePassword("OldPassword123", "ReplacementPassword456"), false);
    assert.equal(changes, 2);
    assert.equal(controller.getSnapshot().status, "unavailable");
    assert.equal(controller.getSnapshot().session, null);
    const warning = controller.getSnapshot().message;
    mode = "readerror"; await controller.revalidateIdentity();
    assert.equal(controller.getSnapshot().status, "unavailable");
    assert.equal(controller.getSnapshot().message, warning);
    mode = "uncertain"; await controller.revalidateIdentity();
    assert.equal(controller.getSnapshot().status, "authenticated");
    assert.equal(controller.getSnapshot().reason, "unknown");
    assert.equal(controller.getSnapshot().message, warning);
    mode = "revoked"; await controller.revalidateIdentity();
    assert.equal(controller.getSnapshot().reason, "unknown");
    assert.equal(controller.getSnapshot().status, "anonymous");
    assert.equal(controller.getSnapshot().message, warning);
    assert.equal(changes, 2);
  } finally { controller.dispose(); }
});

test("role/revision changes advance protected state generation and expiry cannot renew itself", async () => {
  let revision = "1", role: "admin" | "user" = "admin", calls = 0, now = 1_000_000;
  const api = createAuthClient("https://pool.test", async () => { calls++; return Response.json(value(role, revision, 1)); });
  const controller = new SessionController(() => api, () => undefined, () => now);
  try {
    await controller.refresh(); const epoch = controller.getSnapshot().epoch;
    role = "user"; revision = "2"; await controller.refresh();
    assert.ok(controller.getSnapshot().epoch > epoch);
    assert.equal(controller.getSnapshot().session?.user.role, "user");
    now += 61_000; await controller.activity();
    assert.equal(controller.getSnapshot().reason, "expired");
    assert.equal(controller.getSnapshot().session, null);
    assert.equal(calls, 2);
  } finally { controller.dispose(); }
});

test("only foreground activity near the idle deadline renews; ordinary reads do not", async () => {
  let reads = 0, renewals = 0, now = 1_000_000;
  const api = createAuthClient("https://pool.test", async input => {
    const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
    if (path.endsWith("/renew")) { renewals++; return Response.json(value()); }
    reads++; return Response.json(value("admin", "1", 20));
  });
  const controller = new SessionController(() => api, () => undefined, () => now);
  try {
    await controller.refresh(); await controller.refresh();
    assert.equal(renewals, 0);
    await controller.activity(); await controller.activity();
    assert.equal(renewals, 1);
    assert.equal(reads, 2);
    now += 61_000; await controller.activity();
    assert.equal(renewals, 1);
    assert.equal(reads, 3);
  } finally { controller.dispose(); }
});

test("a reply delayed beyond its server deadline cannot mount protected content", async () => {
  let now = 1_000_000;
  const delayed = Promise.withResolvers<Response>();
  const api = createAuthClient("https://pool.test", async () => delayed.promise);
  const controller = new SessionController(() => api, () => undefined, () => now);
  const states: string[] = [];
  controller.subscribe(() => states.push(controller.getSnapshot().status));
  try {
    const restore = controller.refresh();
    now += 61_000;
    delayed.resolve(Response.json(value("admin", "1", 1)));
    await restore;
    assert.equal(controller.getSnapshot().reason, "expired");
    assert.equal(controller.getSnapshot().session, null);
    assert.ok(!states.includes("authenticated"));
  } finally { controller.dispose(); }
});
