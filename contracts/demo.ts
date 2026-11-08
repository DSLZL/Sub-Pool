import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData } from "./auth";

export const demoVersion = "demo-v1";
export const demoScenarioName = z.enum(["overview", "wallet", "restore"]);
const text = z.string().min(1).max(1000);
export const demoOutput = z.object({
  version: z.literal(demoVersion), synthetic: z.literal(true), persistent: z.literal(false), production_effects: z.literal(false),
  scenario: demoScenarioName, title: text, facts: z.array(z.object({ label: text, value: text, unit: text }).strict()).length(3),
  outcome: z.enum(["read_only", "blocked"]), explanation: text, limitations: z.array(text).length(2),
}).strict().refine(value => value.outcome === (value.scenario === "overview" ? "read_only" : "blocked"));
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const demoContract = {
  scenario: base.route({ method: "POST", path: "/api/v1/demo/scenario" })
    .input(z.object({ scenario: demoScenarioName }).strict()).output(demoOutput),
};
export type DemoScenario = z.infer<typeof demoOutput>;
