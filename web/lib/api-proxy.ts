import { authErrorData, authOutputs } from "@sub-pool/contracts/auth";
import { usersOutputs } from "@sub-pool/contracts/users";
import { connectionOutputs } from "@sub-pool/contracts/connection";
import { accountsOutputs } from "@sub-pool/contracts/accounts";
import { monitoringOutputs } from "@sub-pool/contracts/monitoring";
import { participantsOutputs } from "@sub-pool/contracts/participants";
import { poolsOutputs } from "@sub-pool/contracts/pools";
import { observationsOutputs } from "@sub-pool/contracts/observations";
import { estimatesOutputs } from "@sub-pool/contracts/estimates";
import { walletsOutputs } from "@sub-pool/contracts/wallets";
import { walletApplicationsOutputs } from "@sub-pool/contracts/wallet-applications";
import { notificationOutputs } from "@sub-pool/contracts/notifications";
import { temporaryStatus } from "@sub-pool/contracts/temporary";
import { pricingDefault, pricingGroup, pricingGroups } from "@sub-pool/contracts/pricing";
import { dispatchAccounts, dispatchObservation } from "@sub-pool/contracts/dispatch";
import { apiKeysOutputs } from "@sub-pool/contracts/api-keys";
import { securityOutputs } from "@sub-pool/contracts/security";

type Config = { backendOrigin: string; publicOrigin: string; allowLoopbackHTTP: boolean };
const errorStatuses = { BAD_REQUEST: 400, UNAUTHORIZED: 401, FORBIDDEN: 403, SOURCE_BLOCKED: 403, NOT_FOUND: 404, CONFLICT: 409, SELF_SOURCE_PROTECTED: 409, NETWORK_DIAGNOSTICS_DISABLED: 409, OBSERVATION_RANGE_UNVERIFIED: 409, CONNECTION_NOT_CONFIGURED: 409, TOO_MANY_REQUESTS: 429, INTERNAL_SERVER_ERROR: 500, SECRET_STORAGE_UNAVAILABLE: 503 } as const;
const outputs = {
  "/health": monitoringOutputs.health,
  "/api/v1/auth/login": authOutputs.login, "/api/v1/auth/session": authOutputs.session,
  "/api/v1/auth/renew": authOutputs.renew, "/api/v1/auth/logout": authOutputs.logout, "/api/v1/auth/password": authOutputs.password,
  "/api/v1/access": usersOutputs.access, "/api/v1/users/list": usersOutputs.list, "/api/v1/users/get": usersOutputs.get,
  "/api/v1/users/create": usersOutputs.create, "/api/v1/users/update": usersOutputs.update, "/api/v1/users/delete": usersOutputs.delete,
  "/api/v1/connection": connectionOutputs.get, "/api/v1/connection/save": connectionOutputs.save,
  "/api/v1/connection/test": connectionOutputs.test, "/api/v1/connection/discover": connectionOutputs.discover,
  "/api/v1/settings": connectionOutputs.settings, "/api/v1/settings/update": connectionOutputs.updateSettings,
  "/api/v1/accounts/list": accountsOutputs.list, "/api/v1/accounts/get": accountsOutputs.get,
  "/api/v1/accounts/adopt": accountsOutputs.adopt, "/api/v1/accounts/update": accountsOutputs.update,
  "/api/v1/accounts/models": accountsOutputs.models,
  "/api/v1/monitoring/config": monitoringOutputs.config, "/api/v1/monitoring/config/update": monitoringOutputs.updateConfig,
  "/api/v1/monitoring/status": monitoringOutputs.status, "/api/v1/monitoring/collect": monitoringOutputs.collect,
  "/api/v1/monitoring/runs/list": monitoringOutputs.runs, "/api/v1/monitoring/runs/get": monitoringOutputs.run,
  "/api/v1/monitoring/accounts/get": monitoringOutputs.account,
  "/api/v1/participants/candidates": participantsOutputs.candidates,
  "/api/v1/participants/list": participantsOutputs.list, "/api/v1/participants/get": participantsOutputs.participant,
  "/api/v1/participants/create": participantsOutputs.mutation, "/api/v1/participants/update": participantsOutputs.mutation,
  "/api/v1/participants/bindings": participantsOutputs.bindings,
  "/api/v1/participants/usage/captures": participantsOutputs.captures, "/api/v1/participants/usage/view": participantsOutputs.usage,
  "/api/v1/participants/usage/window": participantsOutputs.window,
  "/api/v1/participants/usage/history": participantsOutputs.history,
  "/api/v1/pools/create": poolsOutputs.contract, "/api/v1/pools/update": poolsOutputs.contract,
  "/api/v1/pools/disable": poolsOutputs.contract, "/api/v1/pools/list": poolsOutputs.list,
  "/api/v1/pools/get": poolsOutputs.contract, "/api/v1/pools/contract": poolsOutputs.contract,
  "/api/v1/pools/history": poolsOutputs.history, "/api/v1/pools/usage-evidence": poolsOutputs.evidence,
  "/api/v1/observations/list": observationsOutputs.list, "/api/v1/observations/get": observationsOutputs.get,
  "/api/v1/observations/events": observationsOutputs.events, "/api/v1/observations/state": observationsOutputs.state,
  "/api/v1/observations/review": observationsOutputs.review, "/api/v1/observations/start": observationsOutputs.start,
  "/api/v1/observations/revoke": observationsOutputs.revoke,
  "/api/v1/estimates/current": estimatesOutputs.current, "/api/v1/estimates/record": estimatesOutputs.record,
  "/api/v1/estimates/history": estimatesOutputs.history, "/api/v1/estimates/configure": estimatesOutputs.configure,
  "/api/v1/estimates/recompute": estimatesOutputs.recompute,
  "/api/v1/wallets/list": walletsOutputs.list, "/api/v1/wallets/get": walletsOutputs.get,
  "/api/v1/wallet-applications/list": walletApplicationsOutputs.list,
  "/api/v1/wallet-applications/get": walletApplicationsOutputs.get,
  "/api/v1/wallet-applications/automation": walletApplicationsOutputs.automation,
  "/api/v1/notifications/config": notificationOutputs.configuration,
  "/api/v1/notifications/config/update": notificationOutputs.configuration,
  "/api/v1/notifications/test": notificationOutputs.test,
  "/api/v1/notifications/get": notificationOutputs.get,
  "/api/v1/notifications/list": notificationOutputs.list,
  "/api/v1/temporary/status": temporaryStatus,
  "/api/v1/pricing/groups": pricingGroups, "/api/v1/pricing/group": pricingGroup, "/api/v1/pricing/default": pricingDefault,
  "/api/v1/dispatch/list": dispatchAccounts, "/api/v1/dispatch/account": dispatchObservation,
  "/api/v1/api-keys/access": apiKeysOutputs.access, "/api/v1/api-keys/catalog": apiKeysOutputs.catalog,
  "/api/v1/api-keys/list": apiKeysOutputs.list, "/api/v1/api-keys/create": apiKeysOutputs.create,
  "/api/v1/api-keys/rotate": apiKeysOutputs.rotate, "/api/v1/api-keys/revoke": apiKeysOutputs.revoke,
  "/api/v1/security/public": securityOutputs.publicConfig, "/api/v1/security/config": securityOutputs.config,
  "/api/v1/security/source": securityOutputs.source, "/api/v1/security/config/update": securityOutputs.updateConfig,
  "/api/v1/security/network/compare": securityOutputs.compareNetwork,
  "/api/v1/security/sources/list": securityOutputs.sources, "/api/v1/security/sources/update": securityOutputs.updateSource,
  "/api/v1/security/login-audit": securityOutputs.loginAudit, "/api/v1/security/management-audit": securityOutputs.managementAudit,
};
const reads = new Set(["/api/v1/security/public", "/api/v1/security/config", "/api/v1/security/source", "/api/v1/api-keys/access", "/api/v1/api-keys/catalog", "/health", "/api/v1/auth/session", "/api/v1/access", "/api/v1/connection", "/api/v1/settings", "/api/v1/monitoring/config", "/api/v1/monitoring/status"]);

function trustedOrigin(value: string, allowLoopbackHTTP: boolean) {
  const url = new URL(value);
  if (url.origin !== value || url.username || url.password
    || url.protocol !== "https:" && !(allowLoopbackHTTP && url.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(url.hostname))) {
    throw new Error("Invalid origin configuration");
  }
  return url;
}

function rejected(status: number) {
  const code = status === 400 ? "BAD_REQUEST" : status === 401 ? "UNAUTHORIZED" : status === 403 ? "FORBIDDEN" : null;
  const body = code ? { defined: true, code, status, message: "请求未完成，请核对当前状态。", data: { retry_at: null } }
    : { message: "请求未完成，请核对当前状态。" };
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

async function boundedBody(stream: ReadableStream<Uint8Array> | null, limit: number) {
  const reader = stream?.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  if (reader) {
    try {
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        size += part.value.length;
        if (size > limit) throw new Error("Body exceeds limit");
        chunks.push(part.value);
      }
    } finally {
      await reader.cancel().catch(() => undefined);
      reader.releaseLock();
    }
  }
  const body = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.length; }
  return body;
}

export async function proxyAPI(request: Request, config: Config, fetcher: typeof fetch = fetch): Promise<Response> {
  let backend: URL, origin: URL;
  try {
    backend = trustedOrigin(config.backendOrigin, config.allowLoopbackHTTP);
    origin = trustedOrigin(config.publicOrigin, config.allowLoopbackHTTP);
  } catch { return rejected(503); }
  const url = new URL(request.url);
  if (!Object.hasOwn(outputs, url.pathname)) return rejected(404);
  const outputSchema = outputs[url.pathname as keyof typeof outputs];
  const authentication = url.pathname.startsWith("/api/v1/auth/");
  const securityDomain = url.pathname.startsWith("/api/v1/security/");
  const action = authentication ? url.pathname.slice("/api/v1/auth/".length) : "";
  const participantDomain = url.pathname.startsWith("/api/v1/participants/");
  const poolDomain = url.pathname.startsWith("/api/v1/pools/");
  const observationDomain = url.pathname.startsWith("/api/v1/observations/");
  const estimateDomain = url.pathname.startsWith("/api/v1/estimates/");
  const walletDomain = url.pathname.startsWith("/api/v1/wallets/") || url.pathname.startsWith("/api/v1/wallet-applications/");
  const notificationDomain = url.pathname.startsWith("/api/v1/notifications/");
  const pricingDomain = url.pathname.startsWith("/api/v1/pricing/");
  const connectionDomain = pricingDomain || url.pathname.startsWith("/api/v1/dispatch/") || walletDomain || participantDomain || url.pathname.startsWith("/api/v1/connection") || url.pathname.startsWith("/api/v1/settings") || url.pathname.startsWith("/api/v1/accounts/");
  if (url.search || url.href.endsWith("?")) return rejected(400);
  const method = reads.has(url.pathname) ? "GET" : "POST";
  if (request.method !== method) return rejected(405);
  if (method === "POST" && request.headers.get("origin") !== origin.origin) return rejected(403);
  const cookieName = origin.protocol === "https:" ? "__Host-sub_pool_session" : "sub_pool_dev_session";
  const cookies = (request.headers.get("cookie") ?? "").split(";").map(value => value.trim())
    .filter(value => value.split("=")[0]?.trim() === cookieName);
  const invalidCookie = cookies.length > 1 || cookies.length === 1 && !new RegExp(`^${cookieName}=[A-Za-z0-9_-]{43}$`).test(cookies[0] ?? "");
  if (invalidCookie && !(authentication && action === "login")) {
    const denied = rejected(401);
    denied.headers.set("Set-Cookie", `${cookieName}=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax${origin.protocol === "https:" ? "; Secure" : ""}`);
    return denied;
  }
  const headers = new Headers();
  if (cookies[0] && !invalidCookie) headers.set("Cookie", cookies[0]);
  let body: Uint8Array<ArrayBuffer> | undefined;
  if (method === "POST") {
    const media = request.headers.get("content-type") ?? "";
    if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(media) || request.headers.has("content-encoding")) return rejected(400);
    headers.set("Content-Type", "application/json");
    headers.set("Origin", origin.origin);
    try { body = await boundedBody(request.body, authentication ? 4096 : 65536); } catch { return rejected(400); }
  } else if (request.body) return rejected(400);
  try {
    const response = await fetcher(backend.origin + url.pathname, {
      method, headers, body, cache: "no-store", redirect: "error", credentials: "omit",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(12_000)]),
    });
    // Recorded summaries can contain 20,000 scoped groups; full contracts contain up to 500 members.
    const responseLimit = url.pathname === "/api/v1/participants/usage/view" || url.pathname === "/api/v1/participants/usage/window" || url.pathname === "/api/v1/participants/usage/history" ? 32 * 1024 * 1024 : pricingDomain ? 4 * 1024 * 1024 : participantDomain || poolDomain || observationDomain || estimateDomain || walletDomain || notificationDomain ? 1024 * 1024 : authentication ? 8192 : 262144;
    const raw: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(await boundedBody(response.body, responseLimit)));
    let value: unknown;
    if (response.status === 200) {
      const output = outputSchema.safeParse(raw);
      if (!output.success) return rejected(502);
      value = output.data;
    } else {
      if (typeof raw !== "object" || raw === null || !("code" in raw) || !("data" in raw) || !("status" in raw)
        || !("defined" in raw) || raw.defined !== true || typeof raw.code !== "string" || !Object.hasOwn(errorStatuses, raw.code)
        || errorStatuses[raw.code as keyof typeof errorStatuses] !== response.status || raw.status !== response.status
        || authentication && (raw.code === "NOT_FOUND" || raw.code === "CONFLICT")
        || !observationDomain && raw.code === "OBSERVATION_RANGE_UNVERIFIED"
        || !securityDomain && (raw.code === "SELF_SOURCE_PROTECTED" || raw.code === "NETWORK_DIAGNOSTICS_DISABLED")
        || !connectionDomain && raw.code === "CONNECTION_NOT_CONFIGURED"
        || !connectionDomain && !notificationDomain && raw.code === "SECRET_STORAGE_UNAVAILABLE") return rejected(502);
      const data = authErrorData.safeParse(raw.data);
      if (!data.success) return rejected(502);
      value = { defined: true, code: raw.code, status: response.status, message: "请求未完成，请核对当前状态。", data: data.data };
    }
    const result = Response.json(value, { status: response.status, headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" } });
    const issued = response.headers.getSetCookie();
    if (issued.length > 1) return rejected(502);
    const issuesCookie = authentication && ["login", "logout", "password"].includes(action);
    if (response.status === 200 && issued.length !== Number(issuesCookie)
      || response.status !== 200 && issued.length > 0 && response.status !== 401) return rejected(502);
    if (issued[0]) {
      const cookie = issued[0];
      if (response.status !== 200 && !cookie.startsWith(cookieName + "=;")) return rejected(502);
      if (cookie.length > 512 || !new RegExp(`^${cookieName}=(?:[A-Za-z0-9_-]{43})?;`).test(cookie) || /;\s*domain=/i.test(cookie)
        || !/;\s*path=\/(?:;|$)/i.test(cookie) || !/;\s*httponly(?:;|$)/i.test(cookie)
        || !/;\s*samesite=lax(?:;|$)/i.test(cookie) || origin.protocol === "https:" && !/;\s*secure(?:;|$)/i.test(cookie)) return rejected(502);
      result.headers.append("Set-Cookie", cookie);
    }
    return result;
  } catch { return rejected(502); }
}

export function forwardAPI(request: Request) {
  return proxyAPI(request, {
    backendOrigin: process.env.SUB_POOL_BACKEND_ORIGIN ?? "",
    publicOrigin: process.env.SUB_POOL_PUBLIC_ORIGIN ?? "",
    allowLoopbackHTTP: process.env.SUB_POOL_ALLOW_LOOPBACK_HTTP === "true",
  });
}
