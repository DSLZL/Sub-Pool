import { ORPCError } from "@orpc/client";
import { authErrorData, type Session } from "@sub-pool/contracts/auth";
import type { createAuthClient } from "@sub-pool/contracts/client";
import type { Access } from "@sub-pool/contracts/users";

type API = ReturnType<typeof createAuthClient>;
type Reason = "expired" | "logout" | "password" | "unknown" | null;
export type AuthSnapshot = {
  status: "loading" | "authenticated" | "anonymous" | "unavailable";
  session: Session | null;
  access: Access | null;
  epoch: number;
  busy: boolean;
  reason: Reason;
  message: string | null;
  retryAt: string | null;
};
export const initialAuth: AuthSnapshot = { status: "loading", session: null, access: null, epoch: 0, busy: false, reason: null, message: null, retryAt: null };

export function authFailure(error: unknown, mutation: boolean) {
  const rules = {
    BAD_REQUEST: [400, "请核对输入内容和当前密码。"],
    UNAUTHORIZED: [401, "账号或密码不正确，或会话已失效。"],
    FORBIDDEN: [403, "请求未获授权，请重新核对当前会话。"],
    SOURCE_BLOCKED: [403, "当前来源已被阻断，请联系管理员核对。"],
    TOO_MANY_REQUESTS: [429, "登录暂时受限，请在指定时间后重试。"],
  } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(rules, error.code)) {
    const rule = rules[error.code as keyof typeof rules];
    const data = authErrorData.safeParse(error.data);
    if (rule[0] === error.status && data.success) return { status: rule[0], message: rule[1], retryAt: data.data.retry_at };
  }
  return { status: 0, message: mutation ? "操作结果尚未确认，请恢复会话或重新登录核对，勿重复提交。" : "暂时无法确认会话，请重试恢复会话。", retryAt: null };
}

function identityKey(value: Session | null) {
  return value ? `${value.user.id}:${value.user.revision}:${value.user.role}` : "";
}

// Owns the request generation so aborted or delayed replies cannot restore an old identity.
export class SessionController {
  private snapshot = initialAuth;
  private listeners = new Set<() => void>();
  private generation = 0;
  private abort = new AbortController();
  private expiry: ReturnType<typeof setTimeout> | undefined;
  private acceptedAt = 0;
  private lifetime = 0;
  private checkedActivityAt = 0;

  constructor(private api: () => API, private loadAccess: (signal: AbortSignal) => Promise<Access>,
    private broadcast: (event: "clear" | "refresh") => void = () => undefined, private now = Date.now) {}

  getSnapshot = () => this.snapshot;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };

  private set(update: Partial<AuthSnapshot>) {
    this.snapshot = { ...this.snapshot, ...update };
    for (const listener of this.listeners) listener();
  }

  private begin() {
    this.abort.abort();
    this.abort = new AbortController();
    return { generation: ++this.generation, signal: this.abort.signal, startedAt: this.now() };
  }

  private async accept(value: Session, ticket: { generation: number; signal: AbortSignal; startedAt: number }) {
    if (ticket.generation !== this.generation) return false;
    if (this.snapshot.session && identityKey(value) !== identityKey(this.snapshot.session)) {
      this.set({ status: "loading", session: null, access: null, epoch: this.snapshot.epoch + 1 });
    }
    const access = await this.loadAccess(ticket.signal);
    if (ticket.generation !== this.generation) return false;
    if (value.user.id !== access.user.id || value.user.revision !== access.user.revision
      || value.user.role !== access.user.role || value.user.username !== access.user.username) throw new Error("Session access changed during refresh");
    clearTimeout(this.expiry);
    this.acceptedAt = this.now();
    this.lifetime = Math.max(0, Math.min(Date.parse(value.idle_expires_at), Date.parse(value.absolute_expires_at)) - Date.parse(value.server_time) - Math.max(0, this.acceptedAt - ticket.startedAt));
    if (this.lifetime === 0) { this.invalidate("expired", true); return false; }
    const changed = identityKey(value) !== identityKey(this.snapshot.session) || JSON.stringify(access) !== JSON.stringify(this.snapshot.access);
    const message = this.snapshot.reason === "unknown" || !changed && this.snapshot.status === "authenticated" ? this.snapshot.message : null;
    this.set({ status: "authenticated", session: value, access, busy: false, reason: message && this.snapshot.reason === "unknown" ? "unknown" : null, message, retryAt: null,
      epoch: this.snapshot.epoch + Number(changed) });
    this.expiry = setTimeout(() => this.invalidate("expired", true), this.lifetime);
    return true;
  }

  invalidate(reason: Reason, broadcast = false) {
    this.begin();
    clearTimeout(this.expiry);
    this.set({ status: "anonymous", session: null, access: null, epoch: this.snapshot.epoch + 1, busy: false, reason,
      message: reason === "unknown" && this.snapshot.reason === "unknown" ? this.snapshot.message : null, retryAt: null });
    if (broadcast) this.broadcast("clear");
  }

  dispose() { this.begin(); clearTimeout(this.expiry); }

  async revalidateIdentity() {
    this.invalidate(this.snapshot.reason === "unknown" ? "unknown" : null);
    this.set({ status: "loading" });
    await this.refresh();
  }

  async refresh() {
    if (this.snapshot.busy) return;
    const ticket = this.begin();
    try {
      const value = await this.api().current({}, { signal: ticket.signal });
      await this.accept(value, ticket);
    } catch (error) {
      if (ticket.generation !== this.generation) return;
      const failure = authFailure(error, false);
      if (failure.status === 401) this.invalidate(this.snapshot.reason ?? (this.snapshot.session ? "expired" : null));
      else {
        this.invalidate("unknown");
        this.set({ status: "unavailable", message: this.snapshot.message ?? failure.message });
      }
    }
  }

  async login(username: string, password: string) {
    if (this.snapshot.busy) return false;
    this.invalidate(null);
    const ticket = this.begin();
    this.set({ busy: true });
    this.broadcast("clear");
    try {
      const value = await this.api().login({ username, password }, { signal: ticket.signal });
      if (ticket.generation !== this.generation) return false;
      if (!await this.accept(value, ticket)) return false;
      this.broadcast("refresh");
      return true;
    } catch (error) {
      if (ticket.generation !== this.generation) return false;
      const failure = authFailure(error, true);
      this.set({ status: "anonymous", busy: false, reason: failure.status === 0 ? "unknown" : null, message: failure.message, retryAt: failure.retryAt });
      return false;
    }
  }

  async logout() {
    if (this.snapshot.busy) return false;
    this.invalidate("unknown");
    const ticket = this.begin();
    this.set({ busy: true, message: "退出结果尚未确认，请勿重复提交。" });
    this.broadcast("clear");
    try {
      await this.api().logout({}, { signal: ticket.signal });
      if (ticket.generation !== this.generation) return false;
      this.set({ busy: false, reason: "logout", message: null });
      this.broadcast("clear");
      return true;
    } catch (error) {
      if (ticket.generation !== this.generation) return false;
      this.set({ busy: false, message: authFailure(error, true).message });
      this.broadcast("clear");
      return false;
    }
  }

  async changePassword(current: string, next: string) {
    if (this.snapshot.busy || this.snapshot.status !== "authenticated") return false;
    const ticket = this.begin();
    this.set({ busy: true, message: null });
    try {
      await this.api().changePassword({ current_password: current, new_password: next }, { signal: ticket.signal });
      if (ticket.generation !== this.generation) return false;
      this.invalidate("password", true);
      return true;
    } catch (error) {
      if (ticket.generation !== this.generation) return false;
      const failure = authFailure(error, true);
      if (failure.status === 401) this.invalidate("expired", true);
      else if (failure.status === 0 || failure.status === 403) {
        this.invalidate("unknown", true);
        this.set({ status: "unavailable", message: failure.message });
      } else this.set({ busy: false, message: failure.message });
      return false;
    }
  }

  async activity() {
    if (this.snapshot.status !== "authenticated" || this.snapshot.busy || this.now() - this.checkedActivityAt < 60_000) return;
    this.checkedActivityAt = this.now();
    const value = this.snapshot.session;
    const remaining = this.lifetime - (this.now() - this.acceptedAt);
    if (remaining <= 0) { this.invalidate("expired", true); return; }
    if (!value || remaining > 30 * 60_000 || value.idle_expires_at === value.absolute_expires_at) { await this.refresh(); return; }
    const ticket = this.begin();
    try {
      const renewed = await this.api().renew({}, { signal: ticket.signal });
      await this.accept(renewed, ticket);
    } catch (error) {
      if (ticket.generation !== this.generation) return;
      this.invalidate(authFailure(error, true).status === 401 ? "expired" : "unknown", true);
    }
  }
}
