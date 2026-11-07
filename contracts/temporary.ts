import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData } from "./auth";

export const temporaryVersion = "temporary-v1";
export const temporaryInput = z.object({}).strict();
export const temporaryStatus = z.object({
  can_start: z.literal(false), state: z.literal("unavailable"), reason: z.literal("FINANCIAL_APPLICATION_UNAVAILABLE"),
  round_state: z.literal("not_implemented"), rights_state: z.literal("not_implemented"), reminders_available: z.literal(false),
}).strict();
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const temporaryContract = {
  status: base.route({ method: "POST", path: "/api/v1/temporary/status" }).input(temporaryInput).output(temporaryStatus),
};
export type TemporaryStatus = z.infer<typeof temporaryStatus>;
