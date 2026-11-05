"use client";

import { useApp } from "@/lib/store";
import { accounts, participants, sysUsers } from "@/lib/mock";

/**
 * 演示用的数据范围收窄。真实系统中由服务端按账号范围和参与者范围裁决，
 * 前端仅呈现服务端返回的结果。
 */
export function useScope() {
  const { isAdmin } = useApp();
  const me = sysUsers.find((u) => u.username === "lin.yu")!;
  const accIds = isAdmin ? accounts.map((a) => a.id) : me.accounts;
  const partIds = isAdmin ? participants.map((p) => p.id) : me.participants;
  return {
    isAdmin,
    accounts: accounts.filter((a) => accIds.includes(a.id)),
    participants: participants.filter((p) => partIds.includes(p.id)),
    canAccount: (id: string) => accIds.includes(id),
    canParticipant: (id: string) => partIds.includes(id),
  };
}
