import type { createPricingClient } from "@sub-pool/contracts/client";
import type { PricingGroup, PricingGroups } from "@sub-pool/contracts/pricing";
import { checkText } from "./connection-data";

type Client = ReturnType<typeof createPricingClient>;
export class PricingReadError extends Error {}
export function pricingReadText(check: PricingGroups["read"]) {
  if (check.reason === "GROUP_NOT_VERIFIED") return "尚未验证目标分组，未读取关联账号。";
  return checkText({ ...check, id: "pricing.read", reason: check.reason });
}

export async function readPricingGroups(read: (page: number) => ReturnType<Client["groups"]>, sourceID: string, revision: string) {
  const groups: PricingGroups["groups"] = [], ids = new Set<string>();
  let total: string | null = null;
  // shortcut: The supplied group selector has no paging control; fail above 2,000 visible groups until it does.
  for (let number = 1; number <= 20; number++) {
    const page = await read(number);
    if (page.source_id !== sourceID || page.revision !== revision || page.page !== number) throw new PricingReadError("分组来源或配置版本已变化，请重新读取。");
    if (page.read.state !== "available") return { groups: [], last: page };
    if (page.total === null || total !== null && total !== page.total) throw new PricingReadError("可见分组范围已变化，请重新读取。");
    total = page.total;
    for (const group of page.groups) {
      if (ids.has(group.id)) throw new PricingReadError("分组分页存在重复对象，请重新读取。");
      ids.add(group.id); groups.push(group);
    }
    if (page.next_page === null) {
      if (BigInt(groups.length) !== BigInt(total)) throw new PricingReadError("分组列表未完整读取，请重新读取。");
      return { groups, last: page };
    }
    if (page.next_page !== number + 1) throw new PricingReadError("分组分页无法确认，请重新读取。");
  }
  throw new PricingReadError("可见分组超过当前界面读取范围，未展示部分列表。");
}

export async function readPricingGroup(read: (page: number) => ReturnType<Client["group"]>, sourceID: string, revision: string, groupID: string) {
  const accounts: PricingGroup["accounts"] = [], ids = new Set<string>();
  let total: string | null = null, original: PricingGroup | null = null;
  // shortcut: No account paging control exists here; reject more than 2,000 visible associations.
  for (let number = 1; number <= 20; number++) {
    const page = await read(number);
    if (page.source_id !== sourceID || page.revision !== revision || page.group_id !== groupID || page.page !== number) throw new PricingReadError("所选分组或来源已变化，请重新读取。");
    if (page.read.state !== "available" || page.accounts_read.state !== "available") return { snapshot: page, accounts: [] };
    if (page.total === null || total !== null && total !== page.total || original !== null && (JSON.stringify(original.configuration) !== JSON.stringify(page.configuration) || JSON.stringify(original.group) !== JSON.stringify(page.group) || original.run_mode !== page.run_mode)) throw new PricingReadError("分组配置或关联范围在读取期间变化，请重新读取。");
    total = page.total; original ??= page;
    for (const account of page.accounts) {
      if (ids.has(account.upstream_id)) throw new PricingReadError("关联账号分页存在重复对象，请重新读取。");
      ids.add(account.upstream_id); accounts.push(account);
    }
    if (page.next_page === null) {
      if (BigInt(accounts.length) !== BigInt(total)) throw new PricingReadError("关联账号未完整读取，请重新读取。");
      return { snapshot: page, accounts };
    }
    if (page.next_page !== number + 1) throw new PricingReadError("关联账号分页无法确认，请重新读取。");
  }
  throw new PricingReadError("关联账号超过当前界面读取范围，未展示部分结果。");
}
