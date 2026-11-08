import { test } from "node:test";
import assert from "node:assert/strict";
import { ORPCError } from "@orpc/client";
import type { HelpAnnouncement, HelpCatalog } from "@sub-pool/contracts/help";
import { HelpController, initialHelp } from "./help-data";
import { initialAuth, type AuthSnapshot } from "./session";

const at = "2026-11-08T00:00:00Z";
const announcement: HelpAnnouncement = { id: "current-v1", content_sha256: "a".repeat(64), title: "Current", body: "Original", published_at: at, read_at: null };
const catalog: HelpCatalog = { version: "help-v1", guides: [{ id: "authority", title: "Current guide", minutes: 2, audience: "all", steps: ["Current scope"] }], announcements: [announcement] };
function deferred<T>() {
  let resolve!: (value: T) => void;
  return { promise: new Promise<T>(done => { resolve = done; }), resolve };
}
function harness() {
  let auth: AuthSnapshot = { ...initialAuth, status: "authenticated", epoch: 1,
    session: { user: { id: "a".repeat(32), username: "owner", role: "admin", revision: "1" }, created_at: at, server_time: at, idle_expires_at: at, absolute_expires_at: at } };
  let source = structuredClone(catalog), loads = 0, writes = 0, refreshes = 0;
  const api = {
    catalog: async (_input: object, _options?: { signal?: AbortSignal }) => { loads++; return structuredClone(source); },
    read: async (input: { id: string; content_sha256: string }, _options?: { signal?: AbortSignal }): Promise<HelpAnnouncement> => {
      writes++; assert.equal(input.id, announcement.id); assert.equal(input.content_sha256, announcement.content_sha256);
      return { ...announcement, read_at: at };
    },
  };
  const controller = new HelpController(() => api, () => auth, async () => { refreshes++; });
  controller.reset();
  return { controller, api, get auth() { return auth; }, set auth(value) { auth = value; }, set source(value: HelpCatalog) { source = value; }, get counts() { return { loads, writes, refreshes }; } };
}

test("help single-flight acknowledgement stays unread until the exact native receipt", async () => {
  const h = harness(), wait = deferred<HelpAnnouncement>(); let writes = 0;
  h.api.read = async input => { writes++; assert.deepEqual(input, { id: announcement.id, content_sha256: announcement.content_sha256 }); return wait.promise; };
  await h.controller.load();
  const pending = h.controller.markRead(announcement.id);
  await h.controller.markRead(announcement.id);
  assert.equal(writes, 1); assert.equal(h.controller.getSnapshot().catalog?.announcements[0].read_at, null);
  wait.resolve({ ...announcement, read_at: at }); await pending;
  assert.equal(h.controller.getSnapshot().catalog?.announcements[0].read_at, at);
  assert.equal(h.controller.getSnapshot().pending, null);
  await h.controller.markRead(announcement.id); assert.equal(writes, 1);
});

test("help lost reply permits only read-back until a personal content-bound receipt is confirmed", async () => {
  const h = harness(); let writes = 0;
  h.api.read = async () => { writes++; throw new Error("reply lost after commit"); };
  await h.controller.load(); await h.controller.markRead(announcement.id);
  assert.ok(h.controller.getSnapshot().unknown); assert.equal(h.controller.getSnapshot().catalog?.announcements[0].read_at, null);
  await h.controller.markRead(announcement.id);
  assert.equal(writes, 1); assert.ok(h.controller.getSnapshot().unknown);
  h.source = { ...catalog, announcements: [{ ...announcement, read_at: at }] };
  await h.controller.markRead(announcement.id);
  assert.equal(writes, 1); assert.equal(h.controller.getSnapshot().unknown, null);
  assert.equal(h.controller.getSnapshot().catalog?.announcements[0].read_at, at);
  assert.match(h.controller.getSnapshot().notice!, /只读/);
});

test("help mismatched receipt cannot mark a different version or body read", async () => {
  for (const change of [{ content_sha256: "b".repeat(64) }, { id: "other" }, { body: "changed" }, { read_at: null }]) {
    const h = harness(); h.api.read = async () => ({ ...announcement, read_at: at, ...change });
    await h.controller.load(); await h.controller.markRead(announcement.id);
    assert.equal(h.controller.getSnapshot().catalog?.announcements[0].read_at, null);
    assert.ok(h.controller.getSnapshot().unknown);
  }
});

test("help hides old identity immediately and discards late catalogue and write responses", async () => {
  const h = harness(), read = deferred<HelpCatalog>(); let signal: AbortSignal | undefined;
  h.api.catalog = async (_input, options) => { signal = options?.signal; return read.promise; };
  const pending = h.controller.load();
  h.auth = { ...h.auth, epoch: 2 };
  assert.equal(h.controller.getSnapshot(), initialHelp);
  h.controller.reset(); assert.equal(signal?.aborted, true);
  h.api.catalog = async () => ({ ...catalog, announcements: [] });
  await h.controller.load(); read.resolve(catalog); await pending;
  assert.deepEqual(h.controller.getSnapshot().catalog?.announcements, []);
  h.controller.reset(); h.api.catalog = async () => catalog; await h.controller.load();
  const receipt = deferred<HelpAnnouncement>(); h.api.read = async () => receipt.promise;
  const writing = h.controller.markRead(announcement.id);
  h.auth = { ...h.auth, status: "anonymous", epoch: 3, session: null };
  assert.equal(h.controller.getSnapshot(), initialHelp);
  h.controller.reset(); receipt.resolve({ ...announcement, read_at: at }); await writing;
  assert.equal(h.controller.getSnapshot(), initialHelp);
});

test("help ordinary users cannot acknowledge or expose administrator-only output", async () => {
  const h = harness();
  h.auth = { ...h.auth, epoch: 2, session: { ...h.auth.session!, user: { ...h.auth.session!.user, role: "user" } } };
  h.controller.reset(); await h.controller.load();
  assert.equal(h.controller.getSnapshot().catalog, null);
  await h.controller.markRead(announcement.id); assert.equal(h.counts.writes, 0);
  h.source = { ...catalog, announcements: [] }; await h.controller.load();
  assert.deepEqual(h.controller.getSnapshot().catalog?.announcements, []);
});

test("help defined authority rejection clears protected records and requests session proof", async () => {
  const h = harness();
  h.api.read = async () => { throw new ORPCError("FORBIDDEN", { defined: true, status: 403, data: { retry_at: null } }); };
  await h.controller.load(); await h.controller.markRead(announcement.id);
  assert.equal(h.controller.getSnapshot().catalog, null); assert.equal(h.controller.getSnapshot().unknown, null);
  assert.equal(h.counts.refreshes, 1);
});

test("help failed refresh clears prior catalogue and never supplies mock records", async () => {
  const h = harness(); await h.controller.load(); assert.ok(h.controller.getSnapshot().catalog);
  h.api.catalog = async () => { throw new Error("offline"); }; await h.controller.load();
  assert.equal(h.controller.getSnapshot().catalog, null); assert.ok(h.controller.getSnapshot().notice);
  await h.controller.markRead(announcement.id); assert.equal(h.counts.writes, 0);
});

test("help current empty announcements means zero and unknown IDs never dispatch", async () => {
  const h = harness(); h.source = { ...catalog, announcements: [] }; await h.controller.load();
  assert.deepEqual(h.controller.getSnapshot().catalog?.announcements, []);
  await h.controller.markRead("missing"); assert.equal(h.counts.writes, 0);
});
