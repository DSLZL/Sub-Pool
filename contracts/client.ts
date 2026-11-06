import { createORPCClient } from "@orpc/client";
import type { AnyContractRouter, ContractRouterClient } from "@orpc/contract";
import { RequestValidationPlugin, ResponseValidationPlugin } from "@orpc/contract/plugins";
import type { JsonifiedClient } from "@orpc/openapi-client";
import { OpenAPILink } from "@orpc/openapi-client/fetch";
import { authContract } from "./auth";
import { usersContract } from "./users";
import { connectionContract } from "./connection";
import { accountsContract } from "./accounts";
import { securityContract } from "./security";
import { monitoringContract } from "./monitoring";
import { usageContract } from "./usage";
import { participantsContract } from "./participants";
import { poolsContract } from "./pools";
import { observationsContract } from "./observations";

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

export function createConnectionClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(connectionContract, origin, fetcher);
}

export function createAccountsClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(accountsContract, origin, fetcher);
}

export function createSecurityClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(securityContract, origin, fetcher);
}

export function createMonitoringClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(monitoringContract, origin, fetcher);
}

export function createUsageClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(usageContract, origin, fetcher);
}

export function createParticipantsClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(participantsContract, origin, fetcher);
}

export function createPoolsClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(poolsContract, origin, fetcher);
}

export function createObservationsClient(origin: string, fetcher: typeof fetch = fetch) {
  return createClient(observationsContract, origin, fetcher);
}
