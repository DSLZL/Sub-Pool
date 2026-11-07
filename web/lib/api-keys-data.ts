import type { createAPIKeysClient } from "@sub-pool/contracts/client";
import type { APIKey, APIKeyIssuance, APICatalog } from "@sub-pool/contracts/api-keys";
import type { Access } from "@sub-pool/contracts/users";

type Client = ReturnType<typeof createAPIKeysClient>;
export type KeyMutation = { kind: "create"; name: string; mode: APIKey["mode"] } | { kind: "rotate" | "revoke"; key: APIKey };
export class APIKeyReadError extends Error {}

export async function readAPIKeyPages(client: Pick<Client, "list">, signal?: AbortSignal) {
  const keys: APIKey[] = [], ids = new Set<string>(), cursors = new Set<string>();
  let after = "";
  for (;;) {
    const page = await client.list({ after, limit: 100 }, { signal });
    for (const key of page.keys) {
      if (ids.has(key.id) || keys.length >= 2000) throw new APIKeyReadError("Key 列表重复或超过 2000 条读取上限，未展示部分记录。");
      ids.add(key.id); keys.push(key);
    }
    if (page.next_after === null) return keys;
    if (page.keys.at(-1)?.id !== page.next_after || page.next_after === after || cursors.has(page.next_after)) {
      throw new APIKeyReadError("Key 分页未前进，未展示部分记录。");
    }
    cursors.add(page.next_after); after = page.next_after;
  }
}

export function checkAPIKeyCatalog(catalog: APICatalog, access: Access) {
  const actual = catalog.access;
  if (catalog.credential_mode !== "session" || actual.all_data !== access.all_data
    || (["id", "revision", "role", "username"] as const).some(key => actual.user[key] !== access.user[key])
    || (["features", "account_ids", "participant_ids"] as const).some(key =>
      JSON.stringify([...actual[key]].sort()) !== JSON.stringify([...access[key]].sort()))) {
    throw new APIKeyReadError("当前身份或权限已变化，请重新确认会话后读取。");
  }
  return catalog;
}

export function checkAPIKeyReceipt(input: KeyMutation, value: APIKey | APIKeyIssuance) {
  const key = "key" in value ? value.key : value;
  let valid = input.kind === "revoke" ? !("secret" in value) && key.revoked_at !== null
    : "secret" in value && key.revoked_at === null && key.last_used_at === null;
  if (input.kind === "create") {
    valid &&= key.name === input.name && key.mode === input.mode && key.revision === "1" && key.rotated_at === null;
  } else {
    const before = input.key;
    valid &&= before.revoked_at === null && key.id === before.id && key.name === before.name && key.mode === before.mode
      && key.created_at === before.created_at && BigInt(key.revision) === BigInt(before.revision) + 1n
      && (input.kind === "rotate" ? key.rotated_at !== null : key.rotated_at === before.rotated_at);
  }
  if (!valid) throw new APIKeyReadError("Key 回执与本次操作不一致；结果尚未确认，请回读核对，勿重复提交。");
  return value;
}
