import assert from "node:assert/strict";
import { test } from "node:test";
import { createTemporaryClient } from "./client";
import { temporaryInput, temporaryStatus, temporaryRound, temporaryPreflight, temporarySettlement, temporaryRightsReceipt } from "./temporary";

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

const id = (value: number) => { const bytes = Buffer.alloc(16); bytes[15] = value; return bytes.toString("base64url"); };
const a=id(0), p=id(1), q=id(2), roundID=id(3), source=id(4);
const period={kind:"short",start_at:"2026-01-01T00:00:00Z",end_at:"2026-01-01T00:06:00Z",duration_seconds:"360",basis:"native_reset_and_duration"};
const simulation={protocol:"sub-pool-simulator-v1",environment:"simulated",epoch:id(5)};
const amount=(numerator:string,denominator="1")=>({numerator,denominator});
const scope={simulation,source_id:source,connection_revision:"1",policy_revision:"1",expires_at:"2026-01-01T00:04:00Z",latest_deadline:period.end_at,
 accounts:[{account_id:a,upstream_id:"1",account_revision:"1",contract_id:"1",contract_head:"1",member_head:"members",estimate_revision:"1",management_revision:"1",price_version:"p1",wallet_multiplier:"0.5",
 periods:[period],settlement_period:period,remaining:amount("14"),reserve:amount("2"),members:[{participant_id:p,binding_id:"1",user_id:"21",baseline:amount("6")},{participant_id:q,binding_id:"2",user_id:"22",baseline:amount("6")}]}],
 wallets:[p,q].map((participant_id,i)=>({participant_id,participant_revision:"1",binding_id:String(i+1),connection_revision:"1",source_id:source,latest_id:"1",user_id:String(21+i),recommendation_id:"1",basis_hash:"a".repeat(64)}))};
const round={id:roundID,actor_id:id(6),started_at:"2026-01-01T00:03:00Z",deadline:"2026-01-01T00:05:00Z",mode:"carry",scope,
 caps:[{account_id:a,participant_id:p,amount:"10"},{account_id:a,participant_id:q,amount:"1"}],acknowledged_participants:[p,q],informed:false,exit:null};

test("temporary round rejects unfunded caps, missing owners and extended exits",()=>{
 temporaryRound.parse(round);
 for(const change of [
  {caps:[{...round.caps[0],amount:"14"},round.caps[1]]},
  {caps:[round.caps[0]]},{caps:[round.caps[0],round.caps[0]]},
  {caps:round.caps.map(row=>({...row,amount:"0"}))},
  {acknowledged_participants:[p]},{acknowledged_participants:[p,p]},
  {mode:"no_carry",informed:false},{deadline:"2026-01-01T00:07:00Z"},
  {exit:{exited_at:"2026-01-01T00:05:01Z",recorded_at:"2026-01-01T00:05:02Z",reason:"deadline",actor_id:null}},
 ]) assert.equal(temporaryRound.safeParse({...round,...change}).success,false,JSON.stringify(change));
 assert.equal(temporaryPreflight.safeParse({state:"ready",reason:"CONDITIONAL_SIMULATION_ONLY",scope_hash:"a".repeat(64),scope:{...scope,wallets:[scope.wallets[0]]}}).success,false);
});

test("carry receipt requires a closed tail, immediate next cycle, reconciled spend and zero sum",()=>{
 const next={...period,start_at:period.end_at,end_at:"2026-01-01T00:12:00Z"};
 const value={round_id:roundID,account_id:a,state:"settled",reason:"CARRY_SETTLED",original_period:period,next_period:next,ledger_revision:"2",closed_through:period.end_at,
  round_spent:{[p]:"7",[q]:"0"},period_spent:{[p]:"7",[q]:"1"},adjustments:{[p]:"-1",[q]:"1"},residuals:{}};
 temporarySettlement.parse(value);
 for(const change of [{next_period:null},{closed_through:round.started_at},{ledger_revision:""},{period_spent:{[p]:"6",[q]:"1"}},
  {adjustments:{[p]:"-1",[q]:"0.99"}},{state:"pending_evidence"},{next_period:{...next,start_at:"2026-01-01T00:12:00Z",end_at:"2026-01-01T00:18:00Z"}}])
  assert.equal(temporarySettlement.safeParse({...value,...change}).success,false);
 const receipt={request_id:id(7),round_id:roundID,account_id:a,participant_id:p,binding_id:"1",revision:"0",period,after:"0",reason:"clear funded debit",before:"-0.00000001",delta:"0.00000001",recorded_at:round.started_at};
 assert.ok(temporaryRightsReceipt.safeParse(receipt).success);
 assert.equal(temporaryRightsReceipt.safeParse({...receipt,delta:"0"}).success,false);
 assert.equal(temporaryRightsReceipt.safeParse({...receipt,after:"-0"}).success,false);
});
