import { proxyAuth } from "@/lib/auth-proxy";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

function forward(request: Request) {
  return proxyAuth(request, {
    backendOrigin: process.env.SUB_POOL_BACKEND_ORIGIN ?? "",
    publicOrigin: process.env.SUB_POOL_PUBLIC_ORIGIN ?? "",
    allowLoopbackHTTP: process.env.SUB_POOL_ALLOW_LOOPBACK_HTTP === "true",
  });
}

export const GET = forward;
export const POST = forward;
