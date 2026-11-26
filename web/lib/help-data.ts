import type { createHelpClient } from "@sub-pool/contracts/client";
import { helpOutputs, type HelpAnnouncement, type HelpCatalog } from "@sub-pool/contracts/help";
import type { AuthSnapshot } from "./session";
import { connectionFailure } from "./connection-data";

type API = Pick<ReturnType<typeof createHelpClient>, "catalog" | "read">;
type Receipt = Pick<HelpAnnouncement, "id" | "content_sha256">;
export type HelpState = {
  catalog: HelpCatalog | null;
  pending: "catalog" | "read" | null;
  notice: string | null;
  unknown: Receipt | null;
};
export const initialHelp: HelpState = { catalog: null, pending: null, notice: null, unknown: null };

export class HelpController {
  private state = initialHelp;
  private owner = -1;
  private generation = 0;
  private abort = new AbortController();
  private listeners = new Set<() => void>();

  constructor(private api: () => API, private auth: () => AuthSnapshot, private refreshSession: () => Promise<void>) {}

  private current = (generation = this.generation) =>
    this.generation === generation && this.auth().status === "authenticated" && this.auth().epoch === this.owner;
  getSnapshot = () => this.current() ? this.state : initialHelp;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private set(update: Partial<HelpState>) {
    this.state = { ...this.state, ...update };
    for (const listener of this.listeners) listener();
  }
  reset() {
    this.dispose();
    this.owner = this.auth().epoch;
    this.set(initialHelp);
  }
  dispose() { this.generation++; this.abort.abort(); this.abort = new AbortController(); this.owner = -1; }
  private failure(error: unknown) {
    const result = connectionFailure(error);
    if (result.status === 401 || result.status === 403) void this.refreshSession();
    return result;
  }

  load = async () => {
    if (!this.current() || this.state.pending) return;
    const generation = ++this.generation;
    this.set({ pending: "catalog", catalog: null, notice: null });
    try {
      const catalog = helpOutputs.catalog.parse(await this.api().catalog({}, { signal: this.abort.signal }));
      if (!this.current(generation)) return;
      if (this.auth().session?.user.role !== "admin" && (catalog.announcements.length || catalog.guides.some(g => g.audience === "admin"))) {
        throw new Error("Help response exceeds current authority");
      }
      const unknown = this.state.unknown;
      const confirmed = unknown && catalog.announcements.some(a => a.id === unknown.id && a.content_sha256 === unknown.content_sha256 && a.read_at !== null);
      this.set({ catalog, unknown: confirmed ? null : unknown,
        notice: confirmed ? "已读。" : unknown ? "已读待核对。" : null });
    } catch (error) {
      if (this.current(generation)) {
        const result = this.failure(error);
        this.set({ catalog: null, notice: this.state.unknown ? "请稍后重读。" : result.text });
      }
    } finally { if (this.current(generation)) this.set({ pending: null }); }
  };

  markRead = async (id: string) => {
    if (!this.current() || this.state.pending || this.auth().session?.user.role !== "admin") return;
    if (this.state.unknown) { await this.load(); return; }
    const catalog = this.state.catalog, expected = catalog?.announcements.find(a => a.id === id);
    if (!catalog || !expected || expected.read_at !== null) return;
    const generation = ++this.generation;
    this.set({ pending: "read", notice: null });
    try {
      const receipt = helpOutputs.read.parse(await this.api().read({ id, content_sha256: expected.content_sha256 }, { signal: this.abort.signal }));
      if (!this.current(generation)) return;
      for (const key of ["id", "content_sha256", "title", "published_at", "body"] as const) {
        if (receipt[key] !== expected[key]) throw new Error("Announcement receipt does not match selected content");
      }
      this.set({ catalog: { ...catalog, announcements: catalog.announcements.map(a => a.id === id ? receipt : a) } });
    } catch (error) {
      if (this.current(generation)) {
        const result = this.failure(error), unknown = result.status === 0;
        this.set({ catalog: unknown ? catalog : null, unknown: unknown ? { id, content_sha256: expected.content_sha256 } : null,
          notice: unknown ? "核对已读。" : result.text });
      }
    } finally { if (this.current(generation)) this.set({ pending: null }); }
  };
}
