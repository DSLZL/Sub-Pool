import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { createDemoClient, createHelpClient } from "./client";
import { helpOutputs } from "./help";
import { demoOutput } from "./demo";

const at = "2026-11-08T00:00:00Z";
const announcement = { id: "owned-release-v1", content_sha256: "a".repeat(64), title: "Owned local release", published_at: at, body: "Actual capability limits.", read_at: null };
const catalog = { version: "help-v1", guides: [{ id: "authority", title: "Current authority", audience: "all", minutes: 1, steps: ["Read actual state."] }], announcements: [announcement] };
const demo = { version: "demo-v1", synthetic: true, persistent: false, production_effects: false, scenario: "wallet", title: "Owned zero target",
  facts: [{ label: "Wallet", value: "18.75", unit: "native" }, { label: "Freeze", value: "2.50", unit: "native" }, { label: "Target", value: "0", unit: "native" }],
  outcome: "blocked", explanation: "No write.", limitations: ["Synthetic only.", "No persistence or production effects."] };

test("help contracts reject fabricated acknowledgement and duplicate catalogues", () => {
  assert.ok(helpOutputs.catalog.safeParse(catalog).success);
  assert.equal(helpOutputs.read.safeParse(announcement).success, false);
  assert.ok(helpOutputs.read.safeParse({ ...announcement, read_at: at }).success);
  assert.equal(helpOutputs.catalog.safeParse({ ...catalog, guides: [...catalog.guides, ...catalog.guides] }).success, false);
  assert.equal(helpOutputs.catalog.safeParse({ ...catalog, announcements: [announcement, announcement] }).success, false);
  assert.equal(helpOutputs.catalog.safeParse({ ...catalog, production_enabled: true }).success, false);
});
test("synthetic outputs cannot claim persistence, production effects or a writable zero target", () => {
  assert.ok(demoOutput.safeParse(demo).success);
  for (const change of [{ synthetic: false }, { persistent: true }, { production_effects: true }, { outcome: "read_only" }, { scenario: "production" }]) {
    assert.equal(demoOutput.safeParse({ ...demo, ...change }).success, false);
  }
});
test("help uses exact fixed routes and validates acknowledgement before dispatch", async () => {
  const calls: string[] = [];
  const client = createHelpClient("http://127.0.0.1:8081", async (request, init) => {
    const r = new Request(request, init); calls.push(new URL(r.url).pathname);
    return Response.json(calls.length === 1 ? catalog : { ...announcement, read_at: at });
  });
  assert.equal((await client.catalog({})).announcements[0]?.read_at, null);
  await assert.rejects(client.read({ id: announcement.id, content_sha256: "wrong" }));
  assert.deepEqual(calls, ["/api/v1/help/catalog"]);
  assert.equal((await client.read({ id: announcement.id, content_sha256: announcement.content_sha256 })).read_at, at);
  assert.deepEqual(calls, ["/api/v1/help/catalog", "/api/v1/help/read"]);
});
test("demo transport omits ambient credentials and keeps the independent scenario route", async () => {
  let calls = 0;
  const client = createDemoClient("http://127.0.0.1:8082", async (request, init) => {
    const r = new Request(request, init); calls++;
    assert.equal(init?.credentials, "omit"); assert.equal(init?.redirect, "error"); assert.equal(init?.cache, "no-store");
    assert.equal(r.headers.has("cookie"), false); assert.equal(r.headers.has("authorization"), false);
    assert.equal(new URL(r.url).pathname, "/api/v1/demo/scenario");
    assert.deepEqual(await r.json(), { scenario: "wallet" });
    return Response.json(demo);
  });
  assert.equal((await client.scenario({ scenario: "wallet" })).production_effects, false); assert.equal(calls, 1);
});
test("a lost acknowledgement reply is dispatched once and never falls back to demo", async () => {
  let calls = 0;
  const client = createHelpClient("http://127.0.0.1:8081", async () => { calls++; throw new TypeError("owned lost response"); });
  await assert.rejects(client.read({ id: announcement.id, content_sha256: announcement.content_sha256 }));
  assert.equal(calls, 1);
});
test("defined help errors preserve safe status without leaking raw bodies", async () => {
  const client = createHelpClient("http://127.0.0.1:8081", async () => Response.json({ defined: true, code: "CONFLICT", status: 409, message: "Request could not be completed.", data: { retry_at: null } }, { status: 409 }));
  await assert.rejects(client.read({ id: announcement.id, content_sha256: announcement.content_sha256 }), error => error instanceof ORPCError && error.code === "CONFLICT");
});
