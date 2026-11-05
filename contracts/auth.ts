import { oc } from "@orpc/contract";
import { z } from "zod";

export const authVersion = "auth-v1";
const utc = z.iso.datetime({ offset: false, local: false, precision: null })
  .regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/);
export const authErrorData = z.object({ retry_at: utc.nullable() }).strict();
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData },
  UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});

export const identity = z.object({
  id: z.string().regex(/^[A-Za-z0-9_-]{22}$/),
  username: z.string().regex(/^[a-z0-9][a-z0-9._-]{2,63}$/),
  role: z.enum(["admin", "user"]),
  revision: z.string().regex(/^[1-9]\d{0,18}$/),
}).strict();
function orderedUTC(value: string) {
  const fraction = value[19] === "." ? value.slice(20, -1) : "";
  return value.slice(0, 19) + "." + fraction.padEnd(9, "0") + "Z";
}
export const session = z.object({
  user: identity,
  created_at: utc, idle_expires_at: utc, absolute_expires_at: utc, server_time: utc,
}).strict().refine(value => {
  const created = orderedUTC(value.created_at), idle = orderedUTC(value.idle_expires_at);
  const absolute = orderedUTC(value.absolute_expires_at), now = orderedUTC(value.server_time);
  return created < idle && idle <= absolute && created <= now && now < idle;
}, "Session deadlines are inconsistent");

const password = z.string().min(1).max(128);
const nextPassword = password.refine(value => {
  return [...value].length >= 10 && new TextEncoder().encode(value).length <= 128
    && /[A-Za-z]/.test(value) && /[0-9]/.test(value);
}, "Password needs at least ten characters including a letter and digit");
const empty = z.object({}).strict();
export const authContract = {
  login: base.route({ method: "POST", path: "/api/v1/auth/login" })
    .input(z.object({ username: z.string().min(3).max(256), password }).strict()).output(session),
  current: base.route({ method: "GET", path: "/api/v1/auth/session" }).input(empty).output(session),
  renew: base.route({ method: "POST", path: "/api/v1/auth/renew" }).input(empty).output(session),
  logout: base.route({ method: "POST", path: "/api/v1/auth/logout" }).input(empty)
    .output(z.object({ logged_out: z.literal(true) }).strict()),
  changePassword: base.route({ method: "POST", path: "/api/v1/auth/password" })
    .input(z.object({ current_password: password, new_password: nextPassword }).strict())
    .output(z.object({ password_changed: z.literal(true), sessions_revoked: z.literal(true) }).strict()),
};

export type Identity = z.infer<typeof identity>;
export type Session = z.infer<typeof session>;
