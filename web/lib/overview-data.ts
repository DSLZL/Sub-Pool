import type { PricingOperation } from "@sub-pool/contracts/pricing";
import type { DispatchEvent } from "@sub-pool/contracts/dispatch";

export function overviewReview(isAdmin: boolean, pricing: readonly PricingOperation[] | null, dispatch: readonly DispatchEvent[] | null) {
  const scope = isAdmin ? "所有来源的本地操作记录" : "仅当前授权内账号调度，非全局数量";
  if (dispatch === null || isAdmin && pricing === null) return { count: null, text: `${scope}尚未完整读取；不能推断待核对数量。` };
  const pricingCount = isAdmin && pricing ? pricing.filter(item => item.state === "needs_review").length : 0;
  const dispatchCount = [...new Map(dispatch.map(event => [event.pause.id, event.pause])).values()].filter(pause => pause.state === "needs_review").length;
  return { count: pricingCount + dispatchCount, text: `${scope}：${isAdmin ? `计价 ${pricingCount} · ` : ""}调度 ${dispatchCount}；不含已拒绝余额请求，非远端实时核验。` };
}
