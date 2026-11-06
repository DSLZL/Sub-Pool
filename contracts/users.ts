import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, identity, newPassword, session } from "./auth";

export const usersVersion = "users-v1";
export const readFeature = z.enum([
  "overview", "accounts", "pools", "usage", "observations", "estimates",
  "balances", "temporary", "pricing", "dispatch", "notifications",
]);
export const objectID = z.string().regex(/^[A-Za-z0-9_-]{21}[AQgw]$/);
const revision = z.string().regex(/^[1-9]\d{0,18}$/)
  .refine(value => BigInt(value) <= 9223372036854775807n);
const unique = <T,>(items: T[]) => new Set(items).size === items.length;
export const grants = z.object({
  features: z.array(readFeature).max(11).refine(unique),
  account_ids: z.array(objectID).max(500).refine(unique),
  participant_ids: z.array(objectID).max(500).refine(unique),
}).strict();
export const access = grants.extend({
  user: identity, all_data: z.boolean(),
}).refine(value => value.all_data === (value.user.role === "admin"), "Access role is inconsistent");
export const userSummary = identity.extend({
  id: objectID, role: z.literal("user"), revision,
  enabled: z.boolean(), created_at: session.shape.created_at,
  feature_count: z.number().int().min(0).max(11),
  account_count: z.number().int().min(0).max(500), participant_count: z.number().int().min(0).max(500),
  last_login_at: session.shape.created_at.nullable(),
});
export const managedUser = userSummary.extend(grants.shape).refine(value => value.feature_count === value.features.length
  && value.account_count === value.account_ids.length && value.participant_count === value.participant_ids.length);
const username = z.string().min(3).max(256);
const versionedUser = z.object({ id: objectID, revision }).strict();
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData },
  UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData },
  CONFLICT: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const usersOutputs = {
  access,
  list: z.object({ users: z.array(userSummary).max(100), next: objectID.nullable() }).strict(),
  get: managedUser, create: managedUser, update: managedUser,
  delete: z.object({ deleted: z.literal(true) }).strict(),
};
export const usersContract = {
  access: base.route({ method: "GET", path: "/api/v1/access" })
    .input(z.object({}).strict()).output(access),
  list: base.route({ method: "POST", path: "/api/v1/users/list" })
    .input(z.object({ after: z.union([objectID, z.literal("")]), search: z.string().max(256),
      limit: z.number().int().min(1).max(100) }).strict()).output(usersOutputs.list),
  get: base.route({ method: "POST", path: "/api/v1/users/get" })
    .input(z.object({ id: objectID }).strict()).output(managedUser),
  create: base.route({ method: "POST", path: "/api/v1/users/create" })
    .input(grants.extend({ username, password: newPassword, enabled: z.boolean() })).output(managedUser),
  update: base.route({ method: "POST", path: "/api/v1/users/update" })
    .input(versionedUser.extend({ username, enabled: z.boolean(), ...grants.shape,
      new_password: z.union([z.literal(""), newPassword]) })).output(managedUser),
  delete: base.route({ method: "POST", path: "/api/v1/users/delete" })
    .input(versionedUser.extend({ confirmation: z.string().min(3).max(64) })).output(usersOutputs.delete),
};

export type ReadFeature = z.infer<typeof readFeature>;
export type Access = z.infer<typeof access>;
export type ManagedUser = z.infer<typeof managedUser>;
export type UserSummary = z.infer<typeof userSummary>;
