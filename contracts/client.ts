import { createORPCClient } from "@orpc/client";
import type { ContractRouterClient } from "@orpc/contract";
import { RequestValidationPlugin, ResponseValidationPlugin } from "@orpc/contract/plugins";
import type { JsonifiedClient } from "@orpc/openapi-client";
import { OpenAPILink } from "@orpc/openapi-client/fetch";
import { authContract } from "./auth";

export function createAuthClient(origin: string, fetcher: typeof fetch = fetch) {
  const target = new URL(origin);
  if (target.origin !== origin || target.username || target.password
    || target.protocol !== "https:" && !(target.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(target.hostname))) {
    throw new Error("An exact HTTPS origin or loopback development origin is required");
  }
  const link = new OpenAPILink(authContract, {
    url: origin,
    plugins: [new RequestValidationPlugin(authContract), new ResponseValidationPlugin(authContract)],
    fetch: (request, init) => {
      if (new URL(request.url).origin !== origin) throw new Error("Unexpected API destination");
      return fetcher(request, { ...init, credentials: "same-origin", redirect: "error", cache: "no-store" });
    },
  });
  const client: JsonifiedClient<ContractRouterClient<typeof authContract>> = createORPCClient(link);
  return client;
}
