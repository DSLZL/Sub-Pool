import type { ManagedUser } from "@sub-pool/contracts/users";

type SavedFields = Pick<ManagedUser, "username" | "role" | "enabled" | "features" | "account_ids" | "participant_ids">;

export function checkUserSave(value: ManagedUser, input: SavedFields, previous?: Pick<ManagedUser, "id" | "revision">) {
  const revision = previous ? (BigInt(previous.revision) + 1n).toString() : "1";
  const sameGrants = (["features", "account_ids", "participant_ids"] as const).every(key =>
    JSON.stringify([...value[key]].sort()) === JSON.stringify([...input[key]].sort()));
  if (value.revision !== revision || previous && value.id !== previous.id
    || value.username !== input.username || value.role !== input.role || value.enabled !== input.enabled || !sameGrants) {
    throw new Error("User save acknowledgement does not match submitted identity and grants");
  }
}
