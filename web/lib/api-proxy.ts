import { randomUUID, timingSafeEqual } from "node:crypto";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { isIP } from "node:net";
import { Readable, Writable } from "node:stream";
import { authErrorData, authOutputs, authVersion } from "@sub-pool/contracts/auth";
import { usersOutputs, usersVersion } from "@sub-pool/contracts/users";
import { connectionOutputs, writeGrantUpdate, connectionVersion } from "@sub-pool/contracts/connection";
import { accountsOutputs, accountsVersion } from "@sub-pool/contracts/accounts";
import { monitoringOutputs, monitoringVersion } from "@sub-pool/contracts/monitoring";
import { usageOutputs, usageVersion } from "@sub-pool/contracts/usage";
import { appendRequestId, contractMismatchMessage, requestIdHeader, validRequestId } from "./request-id";
import { participantsOutputs, participantsVersion } from "@sub-pool/contracts/participants";
import { poolsOutputs, poolsVersion } from "@sub-pool/contracts/pools";
import { observationsOutputs, observationsVersion } from "@sub-pool/contracts/observations";
import { estimatesOutputs, estimatesVersion } from "@sub-pool/contracts/estimates";
import { walletsOutputs, walletsVersion } from "@sub-pool/contracts/wallets";
import { walletApplicationsOutputs, walletApplicationsVersion } from "@sub-pool/contracts/wallet-applications";
import { notificationOutputs, notificationsVersion } from "@sub-pool/contracts/notifications";
import { temporaryOutputs, temporaryVersion } from "@sub-pool/contracts/temporary";
import { pricingDefault, pricingGroup, pricingGroups, pricingControl, pricingOperation, pricingHistory, pricingVersion } from "@sub-pool/contracts/pricing";
import { dispatchAccounts, dispatchObservation, dispatchControlOutputs, dispatchControlVersion, dispatchVersion } from "@sub-pool/contracts/dispatch";
import { apiKeysOutputs, apiKeysVersion } from "@sub-pool/contracts/api-keys";
import { securityOutputs, securityVersion } from "@sub-pool/contracts/security";
import { maintenanceOutputs, maintenanceVersion } from "@sub-pool/contracts/maintenance";
import { backupsOutputs, backupsVersion } from "@sub-pool/contracts/backups";
import { helpOutputs, helpVersion } from "@sub-pool/contracts/help";

type Config = { backendOrigin: string; publicOrigin: string; allowLoopbackHTTP: boolean; sourceProxyKey?: string };
const sourceProxyKeyPattern = /^[a-f0-9]{64}$/;
const backupIdentifier = /^[A-Za-z0-9_-]{21}[AQgw]$/;
const backupUpload = "/api/v1/backups/preflight-file";
const backupDownload = "/api/v1/backups/download";
const longBackupPaths = new Set([backupUpload, backupDownload, "/api/v1/backups/export", "/api/v1/backups/apply", "/api/v1/backups/preflight"]);
const errorStatuses = { BAD_REQUEST: 400, UNAUTHORIZED: 401, FORBIDDEN: 403, PASSWORD_CHANGE_REQUIRED: 403, CURRENT_PASSWORD_INVALID: 403, SOURCE_BLOCKED: 403, NOT_FOUND: 404, CONFLICT: 409, LAST_ADMIN_REQUIRED: 409, SELF_SOURCE_PROTECTED: 409, NETWORK_DIAGNOSTICS_DISABLED: 409, OBSERVATION_RANGE_UNVERIFIED: 409, CONNECTION_NOT_CONFIGURED: 409, CONNECTION_REVERIFICATION_REQUIRED: 409, TOO_MANY_REQUESTS: 429, INTERNAL_SERVER_ERROR: 500, SECRET_STORAGE_UNAVAILABLE: 503, BACKUP_INVALID: 400, BACKUP_SCHEMA_INCOMPATIBLE: 409, BACKUP_LIMIT_EXCEEDED: 413, RESTORE_BUSY: 409, PARTICIPANT_EXIT_BLOCKED: 409, POOL_CHANGE_BLOCKED: 409, PARTICIPANT_EXIT_LIMIT: 409, POOL_MEMBER_LIMIT: 409 } as const;
const fullPoolResponses = new Set(["create", "update", "disable", "get", "contract"].map(action => "/api/v1/pools/" + action));
const largeReadResponses = new Set([
  "/api/v1/wallets/get", "/api/v1/wallets/history", "/api/v1/wallets/refresh",
  "/api/v1/estimates/current",
  "/api/v1/temporary/round", "/api/v1/temporary/rights",
]);
type ResponseSchema = { safeParse(input: unknown): { success: true; data: unknown } | { success: false } };
function versionedSchemas(version: string, schemas: Record<string, ResponseSchema>) {
  return Object.fromEntries(Object.entries(schemas).map(([route, schema]) => [route, { version, schema }]));
}

const outputs = {
  ...versionedSchemas(monitoringVersion, {
    "/health": monitoringOutputs.health,
    "/health/ready": monitoringOutputs.readiness,
    "/api/v1/monitoring/config": monitoringOutputs.config, "/api/v1/monitoring/config/update": monitoringOutputs.updateConfig,
    "/api/v1/monitoring/status": monitoringOutputs.status, "/api/v1/monitoring/collect": monitoringOutputs.collect,
    "/api/v1/monitoring/runs/list": monitoringOutputs.runs, "/api/v1/monitoring/runs/get": monitoringOutputs.run,
    "/api/v1/monitoring/accounts/get": monitoringOutputs.account,
  }),
  ...versionedSchemas(authVersion, {
    "/api/v1/auth/login": authOutputs.login, "/api/v1/auth/session": authOutputs.session,
    "/api/v1/auth/renew": authOutputs.renew, "/api/v1/auth/logout": authOutputs.logout, "/api/v1/auth/password": authOutputs.password,
  }),
  ...versionedSchemas(usersVersion, {
    "/api/v1/access": usersOutputs.access, "/api/v1/users/list": usersOutputs.list, "/api/v1/users/get": usersOutputs.get,
    "/api/v1/users/create": usersOutputs.create, "/api/v1/users/update": usersOutputs.update, "/api/v1/users/delete": usersOutputs.delete,
  }),
  ...versionedSchemas(connectionVersion, {
    "/api/v1/connection": connectionOutputs.get, "/api/v1/connection/impact": connectionOutputs.impact, "/api/v1/connection/save": connectionOutputs.save,
    "/api/v1/connection/write-grants": connectionOutputs.writeGrants,
    "/api/v1/connection/write-grants/update": connectionOutputs.writeGrants,
    "/api/v1/connection/test": connectionOutputs.test, "/api/v1/connection/discover": connectionOutputs.discover,
    "/api/v1/settings": connectionOutputs.settings, "/api/v1/settings/update": connectionOutputs.updateSettings,
  }),
  ...versionedSchemas(accountsVersion, {
    "/api/v1/accounts/list": accountsOutputs.list, "/api/v1/accounts/get": accountsOutputs.get,
    "/api/v1/accounts/adopt": accountsOutputs.adopt, "/api/v1/accounts/update": accountsOutputs.update,
    "/api/v1/accounts/models": accountsOutputs.models, "/api/v1/accounts/refresh": accountsOutputs.refresh,
  }),
  ...versionedSchemas(participantsVersion, {
    "/api/v1/participants/candidates": participantsOutputs.candidates,
    "/api/v1/participants/list": participantsOutputs.list, "/api/v1/participants/get": participantsOutputs.participant,
    "/api/v1/participants/create": participantsOutputs.mutation, "/api/v1/participants/update": participantsOutputs.mutation,
    "/api/v1/participants/exit": participantsOutputs.exit, "/api/v1/participants/exit-result": participantsOutputs.exit,
    "/api/v1/participants/delete": participantsOutputs.delete, "/api/v1/participants/delete-result": participantsOutputs.deleteResult,
    "/api/v1/participants/bindings": participantsOutputs.bindings,
    "/api/v1/participants/usage/captures": participantsOutputs.captures, "/api/v1/participants/usage/view": participantsOutputs.usage,
    "/api/v1/participants/usage/window": participantsOutputs.window,
    "/api/v1/participants/usage/history": participantsOutputs.history, "/api/v1/participants/usage/recorded": participantsOutputs.recorded,
  }),
  ...versionedSchemas(usageVersion, {
    "/api/v1/usage/days/queue": usageOutputs.days, "/api/v1/usage/days/status": usageOutputs.days,
  }),
  ...versionedSchemas(poolsVersion, {
    "/api/v1/pools/create": poolsOutputs.contract, "/api/v1/pools/update": poolsOutputs.contract,
    "/api/v1/pools/disable": poolsOutputs.contract, "/api/v1/pools/list": poolsOutputs.list,
    "/api/v1/pools/get": poolsOutputs.contract, "/api/v1/pools/contract": poolsOutputs.contract,
    "/api/v1/pools/members": poolsOutputs.members,
    "/api/v1/pools/history": poolsOutputs.history, "/api/v1/pools/usage-evidence": poolsOutputs.evidence,
    "/api/v1/pools/weekly-wallet": poolsOutputs.weeklyWallet, "/api/v1/pools/weekly-wallet/configure": poolsOutputs.weeklyWallet,
    "/api/v1/pools/weekly-wallet/recommendation": poolsOutputs.weeklyWalletRecommendation,
    "/api/v1/pools/weekly-wallet/apply": poolsOutputs.weeklyWalletSettlement,
  }),
  ...versionedSchemas(observationsVersion, {
    "/api/v1/observations/list": observationsOutputs.list, "/api/v1/observations/get": observationsOutputs.get,
    "/api/v1/observations/list-usage": observationsOutputs.listUsage,
    "/api/v1/observations/estimate-link": observationsOutputs.estimateLink,
    "/api/v1/observations/recommendation-link": observationsOutputs.recommendationLink,
    "/api/v1/observations/usage-evidence": observationsOutputs.usageEvidence,
    "/api/v1/observations/daily-history": observationsOutputs.dailyHistory,
    "/api/v1/observations/events": observationsOutputs.events, "/api/v1/observations/state": observationsOutputs.state,
    "/api/v1/observations/review": observationsOutputs.review, "/api/v1/observations/start": observationsOutputs.start,
    "/api/v1/observations/revoke": observationsOutputs.revoke,
  }),
  ...versionedSchemas(estimatesVersion, {
    "/api/v1/estimates/current": estimatesOutputs.current, "/api/v1/estimates/record": estimatesOutputs.record,
    "/api/v1/estimates/history": estimatesOutputs.history,
    "/api/v1/estimates/weekly-history": estimatesOutputs.weeklyHistory,
  }),
  ...versionedSchemas(walletsVersion, {
    "/api/v1/wallets/list": walletsOutputs.list, "/api/v1/wallets/get": walletsOutputs.get,
    "/api/v1/wallets/history": walletsOutputs.history, "/api/v1/wallets/refresh": walletsOutputs.refresh,
  }),
  ...versionedSchemas(walletApplicationsVersion, {
    "/api/v1/wallet-applications/list": walletApplicationsOutputs.list,
    "/api/v1/wallet-applications/get": walletApplicationsOutputs.get,
    "/api/v1/wallet-applications/adjust": walletApplicationsOutputs.adjust,
    "/api/v1/wallet-applications/reconcile": walletApplicationsOutputs.reconcile,
    "/api/v1/wallet-applications/automation/history": walletApplicationsOutputs.automationHistory,
  }),
  ...versionedSchemas(notificationsVersion, {
    "/api/v1/notifications/config": notificationOutputs.configuration,
    "/api/v1/notifications/config/update": notificationOutputs.configuration,
    "/api/v1/notifications/test": notificationOutputs.test,
    "/api/v1/notifications/get": notificationOutputs.get,
    "/api/v1/notifications/list": notificationOutputs.list,
  }),
  ...versionedSchemas(temporaryVersion, {
    "/api/v1/temporary/round": temporaryOutputs.round, "/api/v1/temporary/rights": temporaryOutputs.rights,
    "/api/v1/temporary/history": temporaryOutputs.history,
  }),
  ...versionedSchemas(pricingVersion, {
    "/api/v1/pricing/groups": pricingGroups, "/api/v1/pricing/group": pricingGroup, "/api/v1/pricing/default": pricingDefault,
    "/api/v1/pricing/control": pricingControl, "/api/v1/pricing/apply": pricingOperation, "/api/v1/pricing/operation": pricingOperation,
    "/api/v1/pricing/history": pricingHistory, "/api/v1/pricing/revert": pricingOperation, "/api/v1/pricing/retry": pricingOperation, "/api/v1/pricing/review": pricingOperation,
  }),
  ...versionedSchemas(dispatchVersion, {
    "/api/v1/dispatch/list": dispatchAccounts, "/api/v1/dispatch/account": dispatchObservation,
  }),
  ...versionedSchemas(dispatchControlVersion, dispatchControlOutputs),
  ...versionedSchemas(apiKeysVersion, {
    "/api/v1/api-keys/access": apiKeysOutputs.access, "/api/v1/api-keys/catalog": apiKeysOutputs.catalog,
    "/api/v1/api-keys/list": apiKeysOutputs.list, "/api/v1/api-keys/create": apiKeysOutputs.create,
    "/api/v1/api-keys/rotate": apiKeysOutputs.rotate, "/api/v1/api-keys/revoke": apiKeysOutputs.revoke,
  }),
  ...versionedSchemas(securityVersion, {
    "/api/v1/security/public": securityOutputs.publicConfig, "/api/v1/security/config": securityOutputs.config,
    "/api/v1/security/source": securityOutputs.source, "/api/v1/security/config/update": securityOutputs.updateConfig,
    "/api/v1/security/network/compare": securityOutputs.compareNetwork,
    "/api/v1/security/sources/list": securityOutputs.sources, "/api/v1/security/sources/update": securityOutputs.updateSource,
    "/api/v1/security/login-audit": securityOutputs.loginAudit, "/api/v1/security/management-audit": securityOutputs.managementAudit,
  }),
  ...versionedSchemas(maintenanceVersion, {
    "/api/v1/maintenance/preflight": maintenanceOutputs.plan, "/api/v1/maintenance/plan": maintenanceOutputs.plan,
    "/api/v1/maintenance/plans": maintenanceOutputs.plans, "/api/v1/maintenance/apply": maintenanceOutputs.plan,
  }),
  ...versionedSchemas(backupsVersion, {
    "/api/v1/backups/export": backupsOutputs.export, "/api/v1/backups/get": backupsOutputs.get,
    "/api/v1/backups/list": backupsOutputs.list, "/api/v1/backups/preflight": backupsOutputs.preflight,
    "/api/v1/backups/plan": backupsOutputs.plan, "/api/v1/backups/apply": backupsOutputs.apply,
    "/api/v1/backups/checks": backupsOutputs.checks, "/api/v1/backups/review": backupsOutputs.review,
    [backupUpload]: backupsOutputs.preflight,
  }),
  ...versionedSchemas(helpVersion, {
    "/api/v1/help/catalog": helpOutputs.catalog, "/api/v1/help/read": helpOutputs.read,
  }),
};
const reads = new Set(["/api/v1/security/public", "/api/v1/security/config", "/api/v1/security/source", "/api/v1/api-keys/access", "/api/v1/api-keys/catalog", "/health", "/health/ready", "/api/v1/auth/session", "/api/v1/access", "/api/v1/connection", "/api/v1/connection/impact", "/api/v1/settings", "/api/v1/monitoring/config", "/api/v1/monitoring/status"]);
reads.add("/api/v1/connection/write-grants");

export function trustedOrigin(value: string, allowLoopbackHTTP: boolean) {
  const url = new URL(value);
  if (url.origin !== value || url.username || url.password
    || url.protocol !== "https:" && !(allowLoopbackHTTP && url.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(url.hostname))) {
    throw new Error("Invalid origin configuration");
  }
  return url;
}

export function rejected(status: number, requestId: string | null = null) {
  const code = status === 400 ? "BAD_REQUEST" : status === 401 ? "UNAUTHORIZED" : status === 403 ? "FORBIDDEN" : null;
  const message = appendRequestId("请求未完成，请核对当前状态。", requestId);
  const body = code ? { defined: true, code, status, message, data: { retry_at: null } }
    : status === 502 && requestId ? { defined: false, code: "BAD_GATEWAY", status, message } : { message };
  const response = Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
  if (requestId) response.headers.set(requestIdHeader, requestId);
  return response;
}

function contractMismatch(route: string, version: string, requestId: string | null) {
  const id = requestId ?? randomUUID().replaceAll("-", "");
  const data = { route, expected_version: version, request_id: id };
  console.error(JSON.stringify({ level: "error", event: "contract_mismatch", ...data }));
  return Response.json({
    defined: false, code: "CONTRACT_MISMATCH", status: 502,
    message: appendRequestId(contractMismatchMessage, id), data,
  }, {
    status: 502, headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", [requestIdHeader]: id },
  });
}

export async function boundedBody(stream: ReadableStream<Uint8Array> | null, limit: number) {
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

function backupRequest(url: URL, headers: Headers, body: Uint8Array | ReadableStream<Uint8Array> | undefined, signal: AbortSignal) {
  return new Promise<Response>((resolve, reject) => {
    const send = url.protocol === "https:" ? httpsRequest : httpRequest;
    const req = send(url, { method: "POST", headers: Object.fromEntries(headers), signal }, incoming => {
      const received = new Headers();
      for (let i = 0; i < incoming.rawHeaders.length; i += 2) received.append(incoming.rawHeaders[i], incoming.rawHeaders[i + 1]);
      const stream = Readable.toWeb(incoming, {
        strategy: { highWaterMark: 65536, size: (part: Uint8Array) => part.byteLength },
      }) as ReadableStream<Uint8Array>;
      try { resolve(new Response(stream, { status: incoming.statusCode, headers: received })); }
      catch (error) { incoming.destroy(); reject(error); }
    });
    req.on("error", reject);
    if (body instanceof ReadableStream) {
      void body.pipeTo(Writable.toWeb(req), { signal }).catch(error => req.destroy(error instanceof Error ? error : undefined));
    } else req.end(body);
  });
}

export async function proxyAPI(request: Request, config: Config, fetcher?: typeof fetch): Promise<Response> {
  let backend: URL, origin: URL;
  try {
    backend = trustedOrigin(config.backendOrigin, config.allowLoopbackHTTP);
    origin = trustedOrigin(config.publicOrigin, config.allowLoopbackHTTP);
  } catch { return rejected(503); }
  const url = new URL(request.url);
  const download = url.pathname === backupDownload;
  const upload = url.pathname === backupUpload;
  const longBackup = longBackupPaths.has(url.pathname);
  const poolWrite = url.pathname === "/api/v1/pools/create" || url.pathname === "/api/v1/pools/update";
  if (!download && !Object.hasOwn(outputs, url.pathname)) return rejected(404);
  const outputContract = outputs[url.pathname];
  const expectedVersion = download ? backupsVersion : outputContract.version;
  const authentication = url.pathname.startsWith("/api/v1/auth/");
  const securityDomain = url.pathname.startsWith("/api/v1/security/");
  const usersDomain = url.pathname.startsWith("/api/v1/users/");
  const updatesWriteGrant = url.pathname === "/api/v1/connection/write-grants/update";
  const backupDomain = url.pathname.startsWith("/api/v1/backups/");
  const action = authentication ? url.pathname.slice("/api/v1/auth/".length) : "";
  const participantDomain = url.pathname.startsWith("/api/v1/participants/");
  const poolDomain = url.pathname.startsWith("/api/v1/pools/");
  const observationDomain = url.pathname.startsWith("/api/v1/observations/");
  const estimateDomain = url.pathname.startsWith("/api/v1/estimates/");
  const walletDomain = url.pathname.startsWith("/api/v1/wallets/") || url.pathname.startsWith("/api/v1/wallet-applications/");
  const notificationDomain = url.pathname.startsWith("/api/v1/notifications/");
  const pricingDomain = url.pathname.startsWith("/api/v1/pricing/");
  const connectionDomain = url.pathname.startsWith("/api/v1/temporary/") || pricingDomain || url.pathname.startsWith("/api/v1/dispatch/") || walletDomain || participantDomain || url.pathname.startsWith("/api/v1/connection") || url.pathname.startsWith("/api/v1/settings") || url.pathname.startsWith("/api/v1/accounts/");
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
  if (config.sourceProxyKey !== undefined) {
    if (!sourceProxyKeyPattern.test(config.sourceProxyKey)) return rejected(503);
    const proof = request.headers.get("x-sub-pool-proxy-key");
    if (!proof || !sourceProxyKeyPattern.test(proof)
      || !timingSafeEqual(Buffer.from(proof, "hex"), Buffer.from(config.sourceProxyKey, "hex"))) return rejected(403);
    const clientIP = request.headers.get("x-sub-pool-client-ip");
    if (!clientIP || clientIP.includes("%") || isIP(clientIP) === 0) return rejected(403);
    headers.set("X-Forwarded-For", clientIP);
  }
  if (cookies[0] && !invalidCookie) headers.set("Cookie", cookies[0]);
  let body: Uint8Array<ArrayBuffer> | ReadableStream<Uint8Array> | undefined;
  let downloadID = "";
  if (method === "POST") {
    const media = request.headers.get("content-type") ?? "";
    if (request.headers.has("content-encoding")) return rejected(400);
    headers.set("Origin", origin.origin);
    if (upload) {
      const id = request.headers.get("x-backup-request-id") ?? "";
      const length = request.headers.get("content-length");
      if (media.toLowerCase() !== "application/octet-stream" || !backupIdentifier.test(id) || !request.body
        || length !== null && (!/^[1-9]\d*$/.test(length) || !Number.isSafeInteger(Number(length)))) return rejected(400);
      headers.set("Content-Type", "application/octet-stream");
      headers.set("X-Backup-Request-ID", id);
      if (length !== null) headers.set("Content-Length", length);
      body = request.body;
    } else {
      const contentType = download ? "application/x-www-form-urlencoded" : "application/json";
      if (!(download
        ? /^application\/x-www-form-urlencoded(?:\s*;\s*charset=utf-8)?$/i.test(media)
        : /^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(media))) return rejected(400);
      headers.set("Content-Type", contentType);
      try {
        body = await boundedBody(request.body, url.pathname === "/api/v1/backups/preflight" ? 16 * 1024 * 1024 : poolWrite ? 2 * 1024 * 1024 : authentication || updatesWriteGrant || download ? 4096 : 65536);
        if (download || updatesWriteGrant) {
          const input = new TextDecoder("utf-8", { fatal: true }).decode(body);
          if (download) {
            const form = new URLSearchParams(input);
            downloadID = form.get("id") ?? "";
            if (!backupIdentifier.test(downloadID) || form.getAll("id").length !== 1
              || form.getAll("plan_id").length > 1 || (form.has("plan_id") && !backupIdentifier.test(form.get("plan_id") ?? ""))
              || [...form.keys()].some(key => key !== "id" && key !== "plan_id")) return rejected(400);
          } else if (!writeGrantUpdate.safeParse(JSON.parse(input)).success) return rejected(400);
        }
      } catch { return rejected(400); }
    }
  } else if (request.body) return rejected(400);
  let requestId: string | null = null;
  const invalidResponse = () => contractMismatch(url.pathname, expectedVersion, requestId);
  try {
    const target = new URL(url.pathname, backend);
    const init: RequestInit & { duplex?: "half" } = {
      method, headers, body, cache: "no-store", redirect: "error", credentials: "omit",
      signal: longBackup ? request.signal : AbortSignal.any([request.signal, AbortSignal.timeout(12_000)]),
    };
    if (upload) init.duplex = "half";
    const response = longBackup && !fetcher ? await backupRequest(target, headers, body, request.signal) : await (fetcher ?? fetch)(target.href, init);
    requestId = validRequestId(response.headers.get(requestIdHeader));
    if (download && response.status === 200) {
      const type = response.headers.get("content-type");
      const size = response.headers.get("content-length") ?? "";
      const digest = response.headers.get("x-backup-sha256") ?? "";
      const disposition = `attachment; filename="sub-pool-${downloadID}.spbak"`;
      if (!response.body || type !== "application/octet-stream" || response.headers.has("content-encoding")
        || response.headers.get("content-disposition") !== disposition || !/^[1-9]\d*$/.test(size)
        || !/^[a-f0-9]{64}$/.test(digest) || response.headers.has("set-cookie")) {
        await response.body?.cancel();
        return rejected(502, requestId);
      }
      const result = new Response(response.body, { headers: {
        "Content-Type": type, "Content-Disposition": disposition, "Content-Length": size,
        "X-Backup-SHA256": digest, "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff",
      } });
      if (requestId) result.headers.set(requestIdHeader, requestId);
      return result;
    }
    // Full contracts carry up to 10,000 names. Member and history pages keep the usual bound.
    const responseLimit = largeReadResponses.has(url.pathname) ? 32 * 1024 * 1024 : url.pathname === "/api/v1/backups/get" || fullPoolResponses.has(url.pathname) ? 16 * 1024 * 1024 : backupDomain && url.pathname.endsWith("/list") ? 1024 * 1024 : url.pathname.startsWith("/api/v1/maintenance/") || url.pathname.startsWith("/api/v1/temporary/") ? 8 * 1024 * 1024 : url.pathname === "/api/v1/participants/usage/view" || url.pathname === "/api/v1/participants/usage/window" || url.pathname === "/api/v1/participants/usage/history" || url.pathname === "/api/v1/participants/usage/recorded" ? 32 * 1024 * 1024 : pricingDomain ? 4 * 1024 * 1024 : participantDomain || poolDomain || observationDomain || estimateDomain || walletDomain || notificationDomain ? 1024 * 1024 : authentication ? 8192 : 262144;
    const bytes = await boundedBody(response.body, responseLimit);
    let raw: unknown;
    try { raw = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); }
    catch { return invalidResponse(); }
    let value: unknown;
    if (url.pathname === "/health/ready") {
      const ready = monitoringOutputs.readiness.safeParse(raw);
      if (!ready.success || response.status !== (ready.data.status === "ok" ? 200 : 503)) return invalidResponse();
      value = ready.data;
    } else if (response.status === 200) {
      if (url.pathname === "/api/v1/estimates/history") {
        // Go serializes the scoped estimate history from typed records. The shared client validates its fields.
        if (typeof raw !== "object" || raw === null || !("account_id" in raw) || !("records" in raw) || !("next_after" in raw)) return invalidResponse();
        value = { account_id: raw.account_id, records: raw.records, next_after: raw.next_after };
      } else {
        const output = outputContract.schema.safeParse(raw);
        if (!output.success) return invalidResponse();
        value = output.data;
      }
    } else {
      if (typeof raw !== "object" || raw === null || !("code" in raw) || !("data" in raw) || !("status" in raw)
        || !("defined" in raw) || raw.defined !== true || typeof raw.code !== "string" || !Object.hasOwn(errorStatuses, raw.code)
        || errorStatuses[raw.code as keyof typeof errorStatuses] !== response.status || raw.status !== response.status
        || authentication && (raw.code === "NOT_FOUND" || raw.code === "CONFLICT")
        || !observationDomain && raw.code === "OBSERVATION_RANGE_UNVERIFIED"
        || !securityDomain && (raw.code === "SELF_SOURCE_PROTECTED" || raw.code === "NETWORK_DIAGNOSTICS_DISABLED")
        || !usersDomain && raw.code === "LAST_ADMIN_REQUIRED"
        || !usersDomain && !updatesWriteGrant && raw.code === "CURRENT_PASSWORD_INVALID"
        || !connectionDomain && ["CONNECTION_NOT_CONFIGURED", "CONNECTION_REVERIFICATION_REQUIRED"].includes(raw.code)
        || !backupDomain && ["BACKUP_INVALID", "BACKUP_SCHEMA_INCOMPATIBLE", "BACKUP_LIMIT_EXCEEDED", "RESTORE_BUSY"].includes(raw.code)
        || !poolWrite && raw.code === "POOL_MEMBER_LIMIT"
        || url.pathname !== "/api/v1/participants/exit" && raw.code === "PARTICIPANT_EXIT_LIMIT"
        || !connectionDomain && !notificationDomain && !backupDomain && raw.code === "SECRET_STORAGE_UNAVAILABLE") return invalidResponse();
      const data = authErrorData.safeParse(raw.data);
      if (!data.success) return invalidResponse();
      value = { defined: true, code: raw.code, status: response.status, message: appendRequestId("请求未完成，请核对当前状态。", requestId), data: data.data };
    }
    const result = Response.json(value, { status: response.status, headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" } });
    if (requestId) result.headers.set(requestIdHeader, requestId);
    const issued = response.headers.getSetCookie();
    if (issued.length > 1) return rejected(502, requestId);
    const issuesCookie = authentication && ["login", "logout", "password"].includes(action);
    if (response.status === 200 && issued.length !== Number(issuesCookie)
      || response.status !== 200 && issued.length > 0 && response.status !== 401) return rejected(502, requestId);
    if (issued[0]) {
      const cookie = issued[0];
      if (response.status !== 200 && !cookie.startsWith(cookieName + "=;")) return rejected(502, requestId);
      if (cookie.length > 512 || !new RegExp(`^${cookieName}=(?:[A-Za-z0-9_-]{43})?;`).test(cookie) || /;\s*domain=/i.test(cookie)
        || !/;\s*path=\/(?:;|$)/i.test(cookie) || !/;\s*httponly(?:;|$)/i.test(cookie)
        || !/;\s*samesite=lax(?:;|$)/i.test(cookie) || origin.protocol === "https:" && !/;\s*secure(?:;|$)/i.test(cookie)) return rejected(502, requestId);
      result.headers.append("Set-Cookie", cookie);
    }
    return result;
  } catch { return rejected(502, requestId); }
}

export function forwardAPI(request: Request) {
  return proxyAPI(request, {
    backendOrigin: process.env.SUB_POOL_BACKEND_ORIGIN ?? "",
    publicOrigin: process.env.SUB_POOL_PUBLIC_ORIGIN ?? "",
    allowLoopbackHTTP: process.env.SUB_POOL_ALLOW_LOOPBACK_HTTP === "true",
    sourceProxyKey: process.env.SUB_POOL_SOURCE_PROXY_KEY,
  });
}
