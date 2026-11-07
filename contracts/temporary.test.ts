import assert from "node:assert/strict";
import { test } from "node:test";
import { createTemporaryClient } from "./client";
import { temporaryInput, temporaryStatus } from "./temporary";

const unavailable = { can_start: false, state: "unavailable", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", round_state: "not_implemented", rights_state: "not_implemented", reminders_available: false };

test("temporary availability has strict empty input and cannot fabricate rounds, rights or reminders", () => {
  assert.ok(temporaryInput.safeParse({}).success);
  for (const value of [{ enabled: true }, { mode: "carry" }, { mode: "no_carry", notified: true }, { target: "0" }, null]) assert.equal(temporaryInput.safeParse(value).success, false);
  assert.ok(temporaryStatus.safeParse(unavailable).success);
  for (const value of [{ ...unavailable, can_start: true }, { ...unavailable, round_state: "active" }, { ...unavailable, rights_state: "settled" }, { ...unavailable, reminders_available: true }, { ...unavailable, accounts: [] }, { ...unavailable, target: "0" }]) assert.equal(temporaryStatus.safeParse(value).success, false);
});

test("temporary client reads the fixed current-admin boundary and rejects fabricated success or denial without retry", async () => {
  let calls = 0;
  const client = createTemporaryClient("http://127.0.0.1:3000", async (input, init) => {
    const request = new Request(input, init);
    calls++; assert.equal(request.url, "http://127.0.0.1:3000/api/v1/temporary/status"); assert.equal(request.method, "POST");
    assert.equal(await request.text(), "{}"); assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error"); assert.equal(init?.credentials, "same-origin");
    return Response.json(unavailable);
  });
  assert.deepEqual(await client.status({}), unavailable); assert.equal(calls, 1);
  const bad = createTemporaryClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ ...unavailable, can_start: true }); });
  await assert.rejects(bad.status({})); assert.equal(calls, 2);
  const denied = createTemporaryClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "Denied", data: {} }, { status: 403 }); });
  await assert.rejects(denied.status({}), (error: unknown) => error instanceof Error && "code" in error && error.code === "FORBIDDEN"); assert.equal(calls, 3);
});
