import { createORPCClient } from "@orpc/client";
import type { AnyContractRouter, ContractRouterClient } from "@orpc/contract";
import { RequestValidationPlugin, ResponseValidationPlugin } from "@orpc/contract/plugins";
import type { JsonifiedClient } from "@orpc/openapi-client";
import { OpenAPILink } from "@orpc/openapi-client/fetch";
import { authContract } from "./auth";
import { usersContract } from "./users";

function createClient<T extends AnyContractRouter>(contract: T, origin: string, fetcher: typeof fetch) {
  const target = new URL(origin);
  if (target.origin !== origin || target.username || target.password
    || target.protocol !== "https:" && !(target.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(target.hostname))) {
    throw new Error("An exact HTTPS origin or loopback development origin is required");
  }
  const link = new OpenAPILink(contract, {
    url: origin,
    plugins: [new RequestValidationPlugin(contract), new ResponseValidationPlugin(contract)],
    fetch: (request, init) => {
      if (new URL(request.url).origin !== origin) throw new Error("Unexpected API destination");
      return fetcher(request, { ...init, credentials: "same-origin", redirect: "error", cache: "no-store" });
    },
  });
  const client: JsonifiedClient<ContractRouterClient<T>> = createORPCClient(link);
  return client;
}

export function createAuthClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(authContract, origin, fetcher);
}

export function createUsersClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(usersContract, origin, fetcher);
}
