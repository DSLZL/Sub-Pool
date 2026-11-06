import { forwardAPI } from "@/lib/api-proxy";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";
export const GET = forwardAPI;
export const POST = forwardAPI;
