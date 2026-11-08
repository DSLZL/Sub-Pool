import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";

export const helpVersion = "help-v1";
const id = z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/);
const digest = z.string().regex(/^[0-9a-f]{64}$/);
const text = z.string().min(1).max(2000);
const guide = z.object({
  id, title: text, minutes: z.number().int().min(1).max(30), audience: z.enum(["all", "admin", "scoped"]),
  steps: z.array(text).min(1).max(8),
}).strict();
export const helpAnnouncement = z.object({
  id, content_sha256: digest, title: text, published_at: session.shape.created_at, body: text,
  read_at: session.shape.created_at.nullable(),
}).strict();
export const helpOutputs = {
  catalog: z.object({
    version: z.literal(helpVersion), guides: z.array(guide).min(1).max(16), announcements: z.array(helpAnnouncement).max(16),
  }).strict().refine(value => new Set(value.guides.map(v => v.id)).size === value.guides.length
    && new Set(value.announcements.map(v => v.id)).size === value.announcements.length),
  read: helpAnnouncement.refine(value => value.read_at !== null),
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const helpContract = {
  catalog: base.route({ method: "POST", path: "/api/v1/help/catalog" }).input(z.object({}).strict()).output(helpOutputs.catalog),
  read: base.route({ method: "POST", path: "/api/v1/help/read" }).input(z.object({ id, content_sha256: digest }).strict()).output(helpOutputs.read),
};
export type HelpCatalog = z.infer<typeof helpOutputs.catalog>;
export type HelpAnnouncement = z.infer<typeof helpAnnouncement>;
