import assert from "node:assert/strict";
import { test } from "node:test";
import { createMutationGuard } from "./use-guarded-mutation";

test("a guarded request serializes dispatch and leaves an uncertain write locked through a failed read", async () => {
  const snapshots: { pending: string | null; blocked: boolean }[] = [];
  const guard = createMutationGuard(snapshot => snapshots.push(snapshot));
  const deactivate = guard.activate();
  let fail: (error: Error) => void = () => assert.fail("write has not started");
  const write = {
    name: "save",
    action: () => new Promise<string>((_, reject) => { fail = reject; }),
    apply: () => assert.fail("uncertain write has no acknowledgement"),
    onFailure: () => guard.setBlocked(true),
  };

  const first = guard.run(write);
  assert.equal(guard.isBusy(), true);
  assert.equal(await guard.run(write), false);
  fail(new Error("response lost"));
  assert.equal(await first, false);
  assert.deepEqual(snapshots.at(-1), { pending: null, blocked: true });
  assert.equal(await guard.run(write), false);
  assert.equal(await guard.run({ ...write, name: "read", recoveryRead: true, action: async () => { throw new Error("read failed"); } }), false);
  assert.deepEqual(snapshots.at(-1), { pending: null, blocked: true });
  assert.equal(await guard.run({
    ...write, name: "read", recoveryRead: true, action: async () => "current state",
    apply: () => guard.setBlocked(false),
  }), true);
  assert.deepEqual(snapshots.at(-1), { pending: null, blocked: false });
  deactivate();
});

test("stopping a scope aborts its request and a late response cannot update or release the next scope", async () => {
  const snapshots: { pending: string | null; blocked: boolean }[] = [];
  const guard = createMutationGuard(snapshot => snapshots.push(snapshot));
  let oldSignal: AbortSignal | undefined;
  let resolveOld: (value: string) => void = () => assert.fail("old read has not started");
  let resolveNew: (value: string) => void = () => assert.fail("current read has not started");
  const deactivate = guard.activate();
  const old = guard.run({
    name: "old read",
    action: signal => {
      oldSignal = signal;
      return new Promise<string>(resolve => { resolveOld = resolve; });
    },
    apply: () => assert.fail("old reply cannot update"),
    onFailure: () => assert.fail("old reply cannot publish an error"),
  });
  deactivate();
  assert.equal(oldSignal?.aborted, true);

  const close = guard.activate();
  const current = guard.run({
    name: "current read",
    action: () => new Promise<string>(resolve => { resolveNew = resolve; }),
    apply: value => assert.equal(value, "current data"),
    onFailure: () => assert.fail("current reply should succeed"),
  });
  resolveOld("old data");
  assert.equal(await old, false);
  assert.equal(guard.isBusy(), true);
  assert.deepEqual(snapshots.at(-1), { pending: "current read", blocked: false });
  resolveNew("current data");
  assert.equal(await current, true);
  close();
});
