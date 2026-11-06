import type { Access } from "@sub-pool/contracts/users";

// Visibility only; every protected request is authorized again by Go.
export function canAccess(permission: string, access: Access | null) {
  if (!access) return false;
  return access.all_data || permission === "all" || permission !== "admin" && access.features.some(feature => feature === permission);
}
