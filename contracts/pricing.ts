import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { capabilityCheck, discoveredAccount } from "./connection";
import { objectID, revision } from "./users";

export const pricingVersion = "pricing-v1";
const page = z.number().int().min(1).max(2147483647);
const text = (limit: number) => z.string().min(1).max(limit).refine(value => new TextEncoder().encode(value).length <= limit && !/[\p{Cc}]/u.test(value));
const decimal = z.string().regex(/^(0|[1-9]\d{0,37})(\.\d{1,36})?$/);
const count = z.string().regex(/^(0|[1-9]\d{0,18})$/).refine(value => BigInt(value) <= 9223372036854775807n);
const total = z.string().regex(/^(0|[1-9]\d*)$/).refine(value => BigInt(value) <= 214748364700n).nullable();
const check = z.object({
  ...capabilityCheck.shape,
  id: z.enum(["run_mode", "pricing.groups.read", "pricing.group.read", "pricing.accounts.read", "pricing.default.read"]),
  reason: z.union([capabilityCheck.shape.reason, z.literal("GROUP_NOT_VERIFIED")]),
}).strict();
const scope = { revision, source_id: objectID };
const context = {
  ...scope, checked_at: session.shape.created_at, run_mode: z.enum(["standard", "simple"]).nullable(), mode_read: check,
  can_apply: z.literal(false), write_reason: z.literal("PRICING_WRITE_UNQUALIFIED"),
};
const currentMode = (value: { run_mode: string | null; mode_read: z.infer<typeof check> }) => value.mode_read.id === "run_mode" && (value.run_mode !== null) === (value.mode_read.state === "available");
export const pricingGroupsInput = z.object({ ...scope, page, search: z.union([z.literal(""), text(100)]) }).strict();
export const pricingGroupInput = z.object({ ...scope, group_id: revision, page }).strict();
export const pricingDefaultInput = z.object({ ...scope, model: text(256) }).strict();
const group = z.object({ id: revision, name: text(512), status: text(64).nullable(), account_count: count.nullable() }).strict();
const amountNames = ["input_price", "output_price", "cache_write_price", "cache_write_1h_price", "cache_read_price", "image_input_price", "image_output_price", "per_request_price", "fast_multiplier", "flex_multiplier"] as const;
const values = z.object({
  input_price: decimal.nullable(), output_price: decimal.nullable(), cache_write_price: decimal.nullable(), cache_write_1h_price: decimal.nullable(), cache_read_price: decimal.nullable(),
  image_input_price: decimal.nullable(), image_output_price: decimal.nullable(), per_request_price: decimal.nullable(), fast_multiplier: decimal.nullable(), flex_multiplier: decimal.nullable(),
}).strict();
const rule = z.object({
  models: z.array(text(256)).min(1).max(100), billing_mode: z.enum(["", "token", "per_request", "image", "video"]), values,
  unreported: z.array(z.enum(amountNames)).max(10), unprojected: z.array(z.enum(["intervals", "time_pricing", "reasoning_effort_multipliers"])).max(3),
}).strict().refine(value => new Set(value.unreported).size === value.unreported.length && new Set(value.unprojected).size === value.unprojected.length && value.unreported.every(name => value.values[name] === null));
const configuration = z.object({
  rate_multiplier: decimal.nullable(), force_openai_fast: z.boolean().nullable(), free_openai_fast: z.boolean().nullable(), long_context_pricing_enabled: z.boolean().nullable(), model_rules: z.array(rule).max(100).nullable(),
  unreported: z.array(z.enum(["rate_multiplier", "force_openai_fast", "free_openai_fast", "long_context_pricing_enabled", "model_pricing"])).max(5), projection: z.literal("base_fields_only"), unit: z.literal("native_config_value"),
}).strict().refine(value => new Set(value.unreported).size === value.unreported.length && configurationFields.every(name => ((name === "model_pricing" ? value.model_rules : value[name]) === null) === value.unreported.includes(name)));
const configurationFields = ["rate_multiplier", "force_openai_fast", "free_openai_fast", "long_context_pricing_enabled", "model_pricing"] as const;
const pagination = { page, total, next_page: page.nullable() };
function validPage(value: { page: number; total: string | null; next_page: number | null }, length: number, available: boolean) {
  if (!available) return value.total === null && value.next_page === null && length === 0;
  if (value.total === null) return false;
  const total = Number(value.total);
  return length === Math.max(0, Math.min(100, total - (value.page - 1) * 100)) && value.next_page === (value.page * 100 < total ? value.page + 1 : null);
}
export const pricingGroups = z.object({ ...context, ...pagination, groups: z.array(group).max(100), read: check }).strict().refine(value => currentMode(value) && value.read.id === "pricing.groups.read" && new Set(value.groups.map(group => group.id)).size === value.groups.length && validPage(value, value.groups.length, value.read.state === "available"));
export const pricingGroup = z.object({
  ...context, ...pagination, group_id: revision, group: group.nullable(), configuration: configuration.nullable(), read: check,
  accounts: z.array(discoveredAccount).max(100), accounts_read: check,
}).strict().refine(value => currentMode(value) && value.read.id === "pricing.group.read" && value.accounts_read.id === "pricing.accounts.read"
  && (value.read.state === "available" ? value.group?.id === value.group_id && value.configuration !== null : value.group === null && value.configuration === null && value.accounts_read.state === "unverified")
  && new Set(value.accounts.map(account => account.upstream_id)).size === value.accounts.length && value.accounts.every(account => account.platform === "openai")
  && validPage(value, value.accounts.length, value.accounts_read.state === "available"));
const defaultValues = values.omit({ per_request_price: true, fast_multiplier: true, flex_multiplier: true });
export const pricingDefault = z.object({
  ...context, model: text(256), found: z.boolean().nullable(), values: defaultValues.nullable(), unreported: z.array(z.enum(amountNames.slice(0, 7))).max(7),
  unit: z.literal("USD_per_token"), projection: z.literal("base_fields_only"), unprojected: z.tuple([z.literal("reasoning_effort_multipliers")]), read: check,
}).strict().refine(value => currentMode(value) && value.read.id === "pricing.default.read" && (value.read.state === "available" ? value.found !== null && (value.found ? value.values !== null : value.values === null) : value.found === null && value.values === null)
  && new Set(value.unreported).size === value.unreported.length && value.unreported.every(name => value.values !== null && name in value.values && Reflect.get(value.values, name) === null));
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData }, SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
});
export const pricingContract = {
  groups: base.route({ method: "POST", path: "/api/v1/pricing/groups" }).input(pricingGroupsInput).output(pricingGroups),
  group: base.route({ method: "POST", path: "/api/v1/pricing/group" }).input(pricingGroupInput).output(pricingGroup),
  default: base.route({ method: "POST", path: "/api/v1/pricing/default" }).input(pricingDefaultInput).output(pricingDefault),
};
export type PricingGroups = z.infer<typeof pricingGroups>;
export type PricingGroup = z.infer<typeof pricingGroup>;
export type PricingDefault = z.infer<typeof pricingDefault>;
