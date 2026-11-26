import { authErrorData } from "@sub-pool/contracts/auth";
import { demoOutput, demoScenarioName } from "@sub-pool/contracts/demo";
import { boundedBody, rejected, trustedOrigin } from "./api-proxy";

type Config = { demoOrigin: string; backendOrigin: string; publicOrigin: string; allowLoopbackHTTP: boolean };
const errors = { BAD_REQUEST: 400, FORBIDDEN: 403, NOT_FOUND: 404, INTERNAL_SERVER_ERROR: 500 } as const;

export async function proxyDemoAPI(request: Request, config: Config, fetcher: typeof fetch = fetch) {
  let target: URL, origin: URL;
  try {
    target = trustedOrigin(config.demoOrigin, config.allowLoopbackHTTP);
    origin = trustedOrigin(config.publicOrigin, config.allowLoopbackHTTP);
    const backend = trustedOrigin(config.backendOrigin, config.allowLoopbackHTTP);
    if (target.origin === backend.origin || target.origin === origin.origin) return rejected(503);
  } catch { return rejected(503); }
  const url = new URL(request.url);
  if (url.pathname !== "/api/v1/demo/scenario") return rejected(404);
  if (url.search || url.href.endsWith("?")) return rejected(400);
  if (request.method !== "POST") return rejected(405);
  if (request.headers.has("cookie") || request.headers.has("authorization") || request.headers.get("origin") !== origin.origin) return rejected(403);
  if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(request.headers.get("content-type") ?? "") || request.headers.has("content-encoding")) return rejected(400);
  let body: Uint8Array<ArrayBuffer>;
  try {
    body = await boundedBody(request.body, 4096);
    const value: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body));
    if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).length !== 1 || !("scenario" in value) || !demoScenarioName.safeParse(value.scenario).success) return rejected(400);
  } catch { return rejected(400); }
  try {
    const response = await fetcher(target.origin + "/api/v1/demo/scenario", {
      method: "POST", headers: { "Content-Type": "application/json" }, body, credentials: "omit", redirect: "error", cache: "no-store",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(12_000)]),
    });
    if (response.headers.getSetCookie().length) return rejected(502);
    const raw: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(await boundedBody(response.body, 32 * 1024)));
    let value: unknown;
    if (response.status === 200) {
      const output = demoOutput.safeParse(raw);
      if (!output.success || output.data.scenario !== JSON.parse(new TextDecoder().decode(body)).scenario) return rejected(502);
      value = output.data;
    } else {
      if (!raw || typeof raw !== "object" || !("defined" in raw) || raw.defined !== true || !("code" in raw) || typeof raw.code !== "string"
        || !Object.hasOwn(errors, raw.code) || !("status" in raw) || raw.status !== response.status
        || errors[raw.code as keyof typeof errors] !== response.status || !("data" in raw) || !authErrorData.safeParse(raw.data).success) return rejected(502);
      value = { defined: true, code: raw.code, status: response.status, message: "演示失败。", data: authErrorData.parse(raw.data) };
    }
    return Response.json(value, { status: response.status, headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" } });
  } catch { return rejected(502); }
}

export function forwardDemoAPI(request: Request) {
  return proxyDemoAPI(request, {
    demoOrigin: process.env.SUB_POOL_DEMO_ORIGIN ?? "", backendOrigin: process.env.SUB_POOL_BACKEND_ORIGIN ?? "",
    publicOrigin: process.env.SUB_POOL_PUBLIC_ORIGIN ?? "", allowLoopbackHTTP: process.env.SUB_POOL_ALLOW_LOOPBACK_HTTP === "true",
  });
}
