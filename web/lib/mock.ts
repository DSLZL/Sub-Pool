/**
 * 合成演示数据（S2A-OPS-008）。全部为虚构，不连接真实 Sub2API、邮件或数据库。
 * null 一律表示“未知 / 来源未返回”，界面不会把它显示为 0。
 */

import type { Cap, Freshness } from "@/components/ui";

export const NOW = "2026-11-04 14:20";
export const TZ = "Asia/Shanghai";

/* ---------------- 连接与能力 ---------------- */

export const connection = {
  baseUrl: "https://gateway.example.internal",
  credentialSet: true,
  credentialHint: "已配置 · 末四位 ••••7Qa2",
  lastTest: { at: "2026-11-04 13:58", result: "success" as const, latencyMs: 182 },
  version: "v0.1.84",
  runMode: "standard" as "standard" | "simple" | null,
  savedAt: "2026-10-28 09:12",
};

export const capabilities: { id: string; name: string; desc: string; state: Cap; note: string }[] = [
  { id: "read.accounts", name: "读取账号列表与状态", desc: "上游账号标识、类型与调度状态", state: "available", note: "最近检查 13:58 通过" },
  { id: "read.quota", name: "读取额度窗口", desc: "短周期与七天窗口已用比例、重置时间", state: "available", note: "仅 OpenAI OAuth 账号适用" },
  { id: "read.usage", name: "读取用量与费用记录", desc: "请求、Token、费用口径与计费类型", state: "available", note: "最近检查 13:58 通过" },
  { id: "read.users", name: "读取用户与钱包余额", desc: "用户身份、余额与冻结余额", state: "available", note: "冻结余额字段可读" },
  { id: "write.balance", name: "写入用户余额", desc: "将合法正数目标提交到用户钱包", state: "gated", note: "钱包整体管理范围尚未确认，写入保持关闭" },
  { id: "write.schedule", name: "修改账号调度状态", desc: "整账号临时暂停与恢复", state: "available", note: "无原生 CAS，恢复前需核对外部改动" },
  { id: "write.pricing", name: "修改分组计价", desc: "FAST 规则、模型价格、长上下文", state: "unverified", note: "需确认 standard 模式下读回生效" },
  { id: "read.models", name: "读取模型配置", desc: "模型目录与账号映射摘要", state: "noperm", note: "当前凭据返回 403" },
];

export const discovered: {
  upstreamId: string;
  name: string;
  type: string;
  status: string;
  managed: boolean;
  reason?: string;
}[] = [
  { upstreamId: "acc_1042", name: "team-alpha-01", type: "OpenAI OAuth", status: "启用", managed: true },
  { upstreamId: "acc_1043", name: "team-alpha-02", type: "OpenAI OAuth", status: "启用", managed: true },
  { upstreamId: "acc_1051", name: "studio-north", type: "OpenAI OAuth", status: "启用", managed: true },
  { upstreamId: "acc_1060", name: "studio-north (shadow)", type: "OpenAI OAuth", status: "启用", managed: false, reason: "影子账号：与 acc_1051 共享订阅，不可作为独立容量纳管" },
  { upstreamId: "acc_1077", name: "lab-east", type: "OpenAI OAuth", status: "异常", managed: true },
  { upstreamId: "acc_1088", name: "backup-key-pool", type: "API Key", status: "启用", managed: false, reason: "非 OAuth 窗口账号，额度核心不适用" },
  { upstreamId: "acc_1092", name: "research-spare", type: "OpenAI OAuth", status: "停用", managed: false },
];

/* ---------------- 账号 ---------------- */

export type Window = { usedPct: number | null; resetAt: string | null; kind: string; source: "active" | "passive" };

export type Account = {
  id: string;
  upstreamId: string;
  name: string;
  localName: string;
  monitoring: boolean;
  enabled: boolean | null;
  schedulable: boolean | null;
  error: string | null;
  rateLimited: boolean | null;
  lastUsed: string | null;
  short: Window;
  week: Window | null;
  freshness: Freshness;
  lastFetch: string;
  lastSourceAt: string | null;
  sourceMode: "active" | "passive";
  pool: string | null;
  plan: string;
  nextRun: string;
  models: { requested: string; mapped: string | null }[] | null;
};

export const accounts: Account[] = [
  {
    id: "a1",
    upstreamId: "acc_1042",
    name: "team-alpha-01",
    localName: "Alpha 主账号",
    monitoring: true,
    enabled: true,
    schedulable: true,
    error: null,
    rateLimited: false,
    lastUsed: "2026-11-04 14:17",
    short: { usedPct: 42, resetAt: "2026-11-04 17:05", kind: "5 小时窗口", source: "active" },
    week: { usedPct: 61, resetAt: "2026-11-08 09:00", kind: "7 天窗口", source: "active" },
    freshness: "fresh",
    lastFetch: "2026-11-04 14:15",
    lastSourceAt: "2026-11-04 14:14",
    sourceMode: "active",
    pool: "p1",
    plan: "Team",
    nextRun: "14:30",
    models: [
      { requested: "gpt-5.2", mapped: "gpt-5.2" },
      { requested: "gpt-5.2-codex", mapped: "gpt-5.2-codex" },
      { requested: "gpt-5-mini", mapped: null },
    ],
  },
  {
    id: "a2",
    upstreamId: "acc_1043",
    name: "team-alpha-02",
    localName: "Alpha 副账号",
    monitoring: true,
    enabled: true,
    schedulable: true,
    error: null,
    rateLimited: true,
    lastUsed: "2026-11-04 14:02",
    short: { usedPct: 93, resetAt: "2026-11-04 15:40", kind: "5 小时窗口", source: "passive" },
    week: { usedPct: 78, resetAt: "2026-11-06 21:30", kind: "7 天窗口", source: "passive" },
    freshness: "fresh",
    lastFetch: "2026-11-04 14:15",
    lastSourceAt: "2026-11-04 14:02",
    sourceMode: "passive",
    pool: "p1",
    plan: "Team",
    nextRun: "14:30",
    models: [{ requested: "gpt-5.2", mapped: "gpt-5.2" }],
  },
  {
    id: "a3",
    upstreamId: "acc_1051",
    name: "studio-north",
    localName: "北区工作室",
    monitoring: true,
    enabled: true,
    schedulable: false,
    error: null,
    rateLimited: false,
    lastUsed: "2026-11-04 11:48",
    short: { usedPct: 12, resetAt: "2026-11-04 18:20", kind: "5 小时窗口", source: "active" },
    week: null,
    freshness: "stale",
    lastFetch: "2026-11-04 13:40",
    lastSourceAt: "2026-11-04 12:10",
    sourceMode: "active",
    pool: "p2",
    plan: "Plus",
    nextRun: "14:40",
    models: null,
  },
  {
    id: "a4",
    upstreamId: "acc_1077",
    name: "lab-east",
    localName: "东区实验",
    monitoring: true,
    enabled: true,
    schedulable: null,
    error: "上游返回 token 刷新失败",
    rateLimited: null,
    lastUsed: null,
    short: { usedPct: null, resetAt: null, kind: "5 小时窗口", source: "active" },
    week: { usedPct: null, resetAt: null, kind: "7 天窗口", source: "active" },
    freshness: "failed",
    lastFetch: "2026-11-04 14:15",
    lastSourceAt: null,
    sourceMode: "active",
    pool: "p2",
    plan: "Plus",
    nextRun: "14:30（退避）",
    models: null,
  },
];

export const jobs = {
  enabled: true,
  intervalMin: 15,
  nearResetBoost: true,
  staleAfterMin: 60,
  lastRun: { at: "2026-11-04 14:15", ok: 2, failed: 1, skipped: 0, conflict: 0, durationS: 8.4 },
  nextRun: "2026-11-04 14:30",
  lastSuccess: "2026-11-04 14:15",
  history: [
    { id: "r-2291", at: "2026-11-04 14:15", trigger: "定时", ok: 2, failed: 1, skipped: 0, note: "lab-east：token 刷新失败" },
    { id: "r-2290", at: "2026-11-04 14:00", trigger: "定时", ok: 3, failed: 1, skipped: 0, note: "lab-east：token 刷新失败" },
    { id: "r-2289", at: "2026-11-04 13:51", trigger: "手动（admin）", ok: 2, failed: 1, skipped: 1, note: "studio-north：上游限流，跳过" },
    { id: "r-2288", at: "2026-11-04 13:45", trigger: "定时", ok: 4, failed: 0, skipped: 0, note: "" },
    { id: "r-2287", at: "2026-11-04 13:30", trigger: "定时", ok: 4, failed: 0, skipped: 0, note: "" },
  ],
};

/* ---------------- 参与者与池 ---------------- */

export type Participant = {
  id: string;
  name: string;
  upstreamUserId: string;
  upstreamEmail: string;
  contact: string;
  status: "active" | "disabled" | "exited";
  note: string;
  balance: number | null;
  frozen: number | null;
  balanceReadAt: string | null;
  recommendation: number | null;
  recState: "ready" | "insufficient" | "stale" | "temporary" | "blocked";
  recReason?: string;
  lastApplied: { at: string; target: number; outcome: string } | null;
  cycleSpend: number | null;
};

export const participants: Participant[] = [
  {
    id: "m1",
    name: "林雨",
    upstreamUserId: "u_3021",
    upstreamEmail: "lin.yu@example.org",
    contact: "lin.yu@example.org",
    status: "active",
    note: "前端小组",
    balance: 18.42,
    frozen: 0.6,
    balanceReadAt: "2026-11-04 14:15",
    recommendation: 24.0,
    recState: "ready",
    lastApplied: { at: "2026-11-04 09:00", target: 22.5, outcome: "confirmed" },
    cycleSpend: 31.77,
  },
  {
    id: "m2",
    name: "周承",
    upstreamUserId: "u_3027",
    upstreamEmail: "zhou.c@example.org",
    contact: "zhou.c@example.org",
    status: "active",
    note: "",
    balance: 6.1,
    frozen: 0,
    balanceReadAt: "2026-11-04 14:15",
    recommendation: 15.5,
    recState: "temporary",
    recReason: "临时放宽轮次 R-07 生效中",
    lastApplied: { at: "2026-11-04 13:00", target: 15.5, outcome: "unknown" },
    cycleSpend: 48.2,
  },
  {
    id: "m3",
    name: "Ana Torres",
    upstreamUserId: "u_3044",
    upstreamEmail: "ana.t@example.org",
    contact: "ana.t@example.org",
    status: "active",
    note: "跨两个池",
    balance: 40.0,
    frozen: 2.35,
    balanceReadAt: "2026-11-04 14:12",
    recommendation: null,
    recState: "insufficient",
    recReason: "studio-north 观测已过期，全局建议不可应用",
    lastApplied: { at: "2026-11-03 21:00", target: 38.0, outcome: "confirmed" },
    cycleSpend: 12.03,
  },
  {
    id: "m4",
    name: "郑一鸣",
    upstreamUserId: "u_3050",
    upstreamEmail: "zym@example.org",
    contact: "",
    status: "active",
    note: "钱包含自助充值",
    balance: 102.3,
    frozen: null,
    balanceReadAt: "2026-11-04 14:15",
    recommendation: 20.0,
    recState: "blocked",
    recReason: "钱包混用自助充值，未确认整体管理范围，仅提供建议",
    lastApplied: null,
    cycleSpend: 9.4,
  },
  {
    id: "m5",
    name: "Kai Müller",
    upstreamUserId: "u_3061",
    upstreamEmail: "kai.m@example.org",
    contact: "kai.m@example.org",
    status: "disabled",
    note: "10 月底退出",
    balance: 0.0,
    frozen: 0,
    balanceReadAt: "2026-11-04 08:00",
    recommendation: null,
    recState: "stale",
    recReason: "参与者已停用",
    lastApplied: { at: "2026-10-31 18:00", target: 5.0, outcome: "failed" },
    cycleSpend: 0,
  },
];

export type Pool = {
  id: string;
  name: string;
  accounts: string[];
  shares: { participant: string; pct: number }[];
  updatedAt: string;
  version: number;
};

export const pools: Pool[] = [
  {
    id: "p1",
    name: "Alpha 共享池",
    accounts: ["a1", "a2"],
    shares: [
      { participant: "m1", pct: 35 },
      { participant: "m2", pct: 35 },
      { participant: "m3", pct: 20 },
    ],
    updatedAt: "2026-10-30 10:12",
    version: 4,
  },
  {
    id: "p2",
    name: "北区与东区",
    accounts: ["a3", "a4"],
    shares: [
      { participant: "m3", pct: 50 },
      { participant: "m4", pct: 40 },
    ],
    updatedAt: "2026-10-21 16:40",
    version: 2,
  },
];

export const shareHistory = [
  { version: 4, pool: "p1", at: "2026-10-30 10:12", by: "admin", change: "林雨 30% → 35%；周承 40% → 35%", effective: "自 2026-10-30 10:12 起的新观测" },
  { version: 3, pool: "p1", at: "2026-10-12 08:30", by: "admin", change: "新增 Ana Torres 20%", effective: "自 2026-10-12 08:30 起的新观测" },
  { version: 2, pool: "p2", at: "2026-10-21 16:40", by: "admin", change: "Kai Müller 退出（20% 释放为未分配）", effective: "自 2026-10-21 16:40 起的新观测" },
];

export const upstreamUsers = [
  { id: "u_3021", email: "lin.yu@example.org", name: "lin.yu", bound: "m1" },
  { id: "u_3027", email: "zhou.c@example.org", name: "zhou.c", bound: "m2" },
  { id: "u_3044", email: "ana.t@example.org", name: "ana.t", bound: "m3" },
  { id: "u_3050", email: "zym@example.org", name: "zym", bound: "m4" },
  { id: "u_3061", email: "kai.m@example.org", name: "kai.m", bound: "m5" },
  { id: "u_3072", email: "qiao.r@example.org", name: "qiao.r", bound: null },
  { id: "u_3080", email: "lin.yu2@example.org", name: "lin.yu", bound: null },
];

/* ---------------- 用量 ---------------- */

export const usageDays = ["10-22", "10-23", "10-24", "10-25", "10-26", "10-27", "10-28", "10-29", "10-30", "10-31", "11-01", "11-02", "11-03", "11-04"];

export const usageTrend: Record<string, (number | null)[]> = {
  m1: [2.1, 3.4, 1.8, 0.9, 4.2, 3.8, 2.6, 3.1, null, 2.4, 3.9, 4.4, 3.2, 1.7],
  m2: [3.8, 4.2, 5.1, 2.2, 3.6, 4.9, 6.2, 5.5, null, 3.3, 4.8, 6.6, 7.1, 4.2],
  m3: [1.1, 0.4, 0.8, 1.6, 1.2, 0.3, 0.9, 1.4, null, 0.7, 1.0, 1.3, 0.6, 0.4],
  m4: [0.6, 0.9, 0.2, 0.4, 1.1, 0.8, 0.5, 0.7, null, 0.3, 0.9, 1.2, 0.8, 0.5],
};

export const usageSummary = {
  requests: 18422,
  input: 41_200_000,
  output: 6_950_000,
  cacheRead: 22_400_000,
  cacheWrite: null as number | null,
  coverage: { from: "2026-10-22 00:00", to: "2026-11-04 14:15", complete: false, gap: "10-30 00:00 – 23:59 采集中断，未补造" },
};

export const costRows: {
  id: string;
  at: string;
  participant: string;
  account: string;
  model: string;
  upstreamModel: string;
  keyId: string;
  requests: number;
  standard: number | null;
  actual: number | null;
  accountCost: number | null;
  billing: "wallet" | "subscription" | "record_only" | "key_window" | "unconfirmed";
}[] = [
  { id: "c1", at: "11-04 14:10", participant: "m2", account: "a2", model: "gpt-5.2-codex", upstreamModel: "gpt-5.2-codex", keyId: "key_9f21", requests: 211, standard: 6.82, actual: 6.82, accountCost: 5.1, billing: "wallet" },
  { id: "c2", at: "11-04 13:55", participant: "m1", account: "a1", model: "gpt-5.2", upstreamModel: "gpt-5.2", keyId: "key_77ab", requests: 94, standard: 2.41, actual: 2.41, accountCost: null, billing: "wallet" },
  { id: "c3", at: "11-04 13:20", participant: "m3", account: "a3", model: "gpt-5.2", upstreamModel: "gpt-5.2", keyId: "key_31c0", requests: 18, standard: 0.52, actual: 0.0, accountCost: 0.4, billing: "subscription" },
  { id: "c4", at: "11-04 12:48", participant: "m4", account: "a3", model: "gpt-5-mini", upstreamModel: "gpt-5-mini", keyId: "key_a002", requests: 40, standard: 0.18, actual: 0.18, accountCost: null, billing: "unconfirmed" },
  { id: "c5", at: "11-04 11:02", participant: "m2", account: "a1", model: "gpt-5.2-codex", upstreamModel: "gpt-5.2-codex", keyId: "key_9f21", requests: 156, standard: 4.9, actual: 4.9, accountCost: 3.7, billing: "wallet" },
  { id: "c6", at: "11-04 10:40", participant: "—", account: "a1", model: "gpt-5.2", upstreamModel: "gpt-5.2", keyId: "—", requests: 37, standard: 1.2, actual: null, accountCost: null, billing: "record_only" },
];

export const BILLING_LABEL: Record<string, { label: string; tone: "ok" | "info" | "muted" | "warn" | "unk" }> = {
  wallet: { label: "钱包扣除", tone: "ok" },
  subscription: { label: "订阅额度消耗", tone: "info" },
  record_only: { label: "仅记录", tone: "muted" },
  key_window: { label: "仅 Key 窗口", tone: "warn" },
  unconfirmed: { label: "扣除无法确认", tone: "unk" },
};

export const attribution = {
  totalStandard: 214.6,
  attributed: 196.3,
  unattributed: 18.3,
  reasons: [
    { label: "未绑定参与者的上游用户消费", amount: 9.8, confirmed: true },
    { label: "无 Key 元数据的历史请求", amount: 4.1, confirmed: true },
    { label: "窗口百分比与费用差异（可能存在实例外使用）", amount: null, confirmed: false },
  ],
};

export const dailyClose = [
  { day: "11-03", account: "a1", usedPct: 54, spend: 38.4, perPct: 0.71, samples: 96 },
  { day: "11-02", account: "a1", usedPct: 47, spend: 33.1, perPct: 0.7, samples: 96 },
  { day: "11-01", account: "a1", usedPct: 36, spend: 27.0, perPct: 0.75, samples: 94 },
  { day: "10-31", account: "a1", usedPct: 22, spend: null, perPct: null, samples: 12 },
  { day: "10-30", account: "a1", usedPct: null, spend: null, perPct: null, samples: 0 },
];

/* ---------------- 观测 ---------------- */

export type Observation = {
  id: string;
  at: string;
  account: string;
  shortPct: number | null;
  weekPct: number | null;
  spend: number | null;
  source: "active" | "passive";
  state: "valid" | "excluded" | "anchor";
  quality: "good" | "partial" | "stale";
  excludeReason?: string;
};

export const observations: Observation[] = [
  { id: "ob-88412", at: "11-04 14:15", account: "a1", shortPct: 42, weekPct: 61, spend: 152.3, source: "active", state: "valid", quality: "good" },
  { id: "ob-88411", at: "11-04 14:15", account: "a2", shortPct: 93, weekPct: 78, spend: 201.9, source: "passive", state: "valid", quality: "good" },
  { id: "ob-88410", at: "11-04 14:15", account: "a4", shortPct: null, weekPct: null, spend: null, source: "active", state: "valid", quality: "partial" },
  { id: "ob-88405", at: "11-04 14:00", account: "a1", shortPct: 40, weekPct: 60, spend: 149.8, source: "active", state: "valid", quality: "good" },
  { id: "ob-88399", at: "11-04 13:45", account: "a1", shortPct: 3, weekPct: 59, spend: 147.2, source: "active", state: "excluded", quality: "good", excludeReason: "上游短暂返回异常低值，与费用增长不一致" },
  { id: "ob-88392", at: "11-04 13:40", account: "a3", shortPct: 12, weekPct: null, spend: 44.1, source: "active", state: "valid", quality: "stale" },
  { id: "ob-88380", at: "11-04 13:30", account: "a1", shortPct: 37, weekPct: 58, spend: 145.0, source: "active", state: "valid", quality: "good" },
  { id: "ob-86001", at: "11-01 09:05", account: "a1", shortPct: 2, weekPct: 1, spend: 0.4, source: "active", state: "anchor", quality: "good" },
];

/* ---------------- 估算 ---------------- */

export const estimates = [
  {
    account: "a1",
    status: "ok" as const,
    cycle: "2026-11-01 09:00 → 11-08 09:00",
    capacity: { low: 228, mid: 249, high: 268 },
    remaining: { low: 89, mid: 97, high: 105 },
    basis: "61% 已用 / 152.30 USD 标准费用 · 92 个有效观测",
    at: "2026-11-04 14:15",
  },
  {
    account: "a2",
    status: "ok" as const,
    cycle: "2026-10-30 21:30 → 11-06 21:30",
    capacity: { low: 244, mid: 259, high: 281 },
    remaining: { low: 54, mid: 57, high: 62 },
    basis: "78% 已用 / 201.90 USD 标准费用 · 110 个有效观测",
    at: "2026-11-04 14:15",
  },
  { account: "a3", status: "stale" as const, cycle: "七天窗口未返回", capacity: null, remaining: null, basis: "最近有效源观测 12:10，超过过期阈值 60 分钟", at: "2026-11-04 13:40" },
  { account: "a4", status: "insufficient" as const, cycle: "未知", capacity: null, remaining: null, basis: "连续 3 次采集失败，无有效窗口数据", at: null },
];

export const EST_STATUS: Record<string, { label: string; tone: "ok" | "warn" | "unk" | "bad" | "muted" }> = {
  ok: { label: "可估计", tone: "ok" },
  insufficient: { label: "样本不足", tone: "unk" },
  stale: { label: "来源过期", tone: "warn" },
  changed: { label: "前提已变化", tone: "warn" },
  unavailable: { label: "结果不可用", tone: "bad" },
};

export const estimateHistory = [
  { at: "11-04 14:15", account: "a1", mid: 249, change: "+3", reason: "新增 4 个有效观测" },
  { at: "11-04 12:00", account: "a1", mid: 246, change: "−8", reason: "排除 ob-88399 后重算" },
  { at: "11-03 21:00", account: "a1", mid: 254, change: "+0", reason: "无变化" },
  { at: "11-02 10:30", account: "a1", mid: 254, change: "新", reason: "计价前提变化：FAST 规则调整（本系统托管变更 PC-12）" },
];

/* ---------------- 余额应用 ---------------- */

export type BalanceOp = { id: string; at: string; actor: string; participant: string; rec: string; before: number | null; target: number; after: number | null; outcome: string; note: string };

export const balanceOps: BalanceOp[] = [
  { id: "bo-5521", at: "2026-11-04 13:00", actor: "自动任务", participant: "m2", rec: "rec-9902", before: 4.1, target: 15.5, after: null, outcome: "unknown", note: "上游超时；未取得读回证据" },
  { id: "bo-5519", at: "2026-11-04 09:00", actor: "admin", participant: "m1", rec: "rec-9871", before: 12.0, target: 22.5, after: 22.5, outcome: "confirmed", note: "读回一致" },
  { id: "bo-5512", at: "2026-11-03 21:00", actor: "API: ops-bot", participant: "m3", rec: "rec-9840", before: 31.2, target: 38.0, after: 38.0, outcome: "confirmed", note: "" },
  { id: "bo-5508", at: "2026-10-31 18:00", actor: "admin", participant: "m5", rec: "rec-9790", before: 0.0, target: 5.0, after: 0.0, outcome: "failed", note: "上游 403：缺余额写权限" },
  { id: "bo-5501", at: "2026-10-31 12:00", actor: "自动任务", participant: "m4", rec: "rec-9781", before: null, target: 20.0, after: null, outcome: "skipped", note: "钱包混用，自动应用跳过" },
];

export const autoApply = {
  enabled: false,
  lastRun: "2026-11-04 13:00",
  results: { applied: 0, skipped: 3, unknown: 1, failed: 0 },
};

/* ---------------- 临时放宽 ---------------- */

export const tempRound = {
  id: "R-07",
  mode: "carry" as "carry" | "no_carry",
  status: "active" as "active" | "ended" | "terminated",
  startedAt: "2026-11-03 19:00",
  startedBy: "admin",
  endBasis: "最早已确定截止：team-alpha-02 7 天窗口 2026-11-06 21:30",
  fullReminder: true,
  accounts: [
    { account: "a1", cycleEnd: "2026-11-08 09:00", progress: "进行中", consumed: 18.4, state: "pending" },
    { account: "a2", cycleEnd: "2026-11-06 21:30", progress: "进行中（最早截止）", consumed: 26.1, state: "pending" },
  ],
  affected: ["m1", "m2", "m3"],
};

export const tempSettlements = [
  { id: "ts-301", round: "R-06", account: "a1", cycle: "10-25 → 11-01", member: "m2", adj: -6.4, state: "confirmed", reason: "结转扣减已计入 11-01 周期建议", evidenceAt: "2026-11-01 09:05" },
  { id: "ts-302", round: "R-06", account: "a2", cycle: "10-23 → 10-30", member: "m2", adj: -3.1, state: "confirmed", reason: "", evidenceAt: "2026-10-30 21:35" },
  { id: "ts-303", round: "R-06", account: "a1", cycle: "10-25 → 11-01", member: "m1", adj: 2.2, state: "confirmed", reason: "结转补偿", evidenceAt: "2026-11-01 09:05" },
  { id: "ts-304", round: "R-06", account: "a3", cycle: "10-24 → 10-31", member: "m3", adj: null, state: "blocked", reason: "周期末 6 小时缺少观测，不补造尾部消费", evidenceAt: "2026-10-31 02:10" },
];

/* ---------------- 上游计价 ---------------- */

export const pricingGroups = [
  { id: "g-openai-team", name: "openai-team", accounts: ["a1", "a2"], users: 14, visibleLinks: "完整", fast: "1.5×（本系统托管）", longCtx: "开启 · 阈值 272k", models: 6, source: "本系统修改 PC-12", runMode: "standard" },
  { id: "g-openai-north", name: "openai-north", accounts: ["a3"], users: 6, visibleLinks: "部分可见", fast: "继承默认", longCtx: "未知", models: 4, source: "上游默认", runMode: "standard" },
  { id: "g-shared-misc", name: "shared-misc", accounts: ["a3", "a4"], users: 31, visibleLinks: "部分可见", fast: "继承默认", longCtx: "关闭", models: 9, source: "外部修改（10-29 检测到）", runMode: null },
];

export const modelPrices = [
  { model: "gpt-5.2", input: 1.25, output: 10.0, cache: 0.125, source: "上游默认" },
  { model: "gpt-5.2-codex", input: 1.25, output: 10.0, cache: 0.125, source: "本系统修改 PC-12" },
  { model: "gpt-5-mini", input: 0.25, output: 2.0, cache: null, source: "上游默认" },
  { model: "gpt-5.2-pro", input: null, output: null, cache: null, source: "缺价（不补 0）" },
];

export const pricingChanges = [
  {
    id: "PC-14",
    at: "2026-11-04 10:20",
    by: "admin",
    kind: "模型价格",
    targets: [
      { group: "openai-team", state: "confirmed", note: "读回一致" },
      { group: "shared-misc", state: "conflict", note: "检测到外部改动，已停止" },
    ],
    revertable: false,
  },
  {
    id: "PC-12",
    at: "2026-11-02 10:30",
    by: "admin",
    kind: "FAST 规则",
    targets: [{ group: "openai-team", state: "confirmed", note: "读回一致" }],
    revertable: true,
  },
  {
    id: "PC-09",
    at: "2026-10-27 15:00",
    by: "admin",
    kind: "长上下文",
    targets: [
      { group: "openai-team", state: "confirmed", note: "" },
      { group: "openai-north", state: "unknown", note: "写入超时，读回失败" },
    ],
    revertable: false,
  },
];

/* ---------------- 整账号暂停 ---------------- */

export const pauses = [
  { id: "ds-41", account: "a3", state: "active", from: "2026-11-04 11:50", until: "2026-11-04 16:00", prev: "可调度", by: "admin", result: "confirmed", note: "临时维护" },
  { id: "ds-38", account: "a2", state: "restored", from: "2026-11-02 08:00", until: "2026-11-02 12:00", prev: "可调度", by: "admin", result: "confirmed", note: "到期自动恢复，读回一致" },
  { id: "ds-35", account: "a4", state: "reconcile", from: "2026-11-01 20:00", until: "2026-11-02 02:00", prev: "可调度", by: "admin", result: "reconcile", note: "恢复前检测到外部修改调度状态，停止自动恢复" },
];

/* ---------------- 通知 ---------------- */

export const notifyChannel = {
  kind: "smtp" as "smtp" | "resend",
  host: "smtp.example.org",
  port: 587,
  user: "notify@example.org",
  secretSet: true,
  from: "Sub-Pool <notify@example.org>",
  to: "ops@example.org",
  lastTest: { at: "2026-11-01 10:00", outcome: "success" },
};

export const notifyKinds = [
  { id: "low_balance", label: "余额低且存在补充建议", desc: "仅在建议完整可用时发送", on: true },
  { id: "rec_change", label: "建议变化", desc: "建议值出现有业务意义的变化", on: true },
  { id: "est_change", label: "估算显著变化", desc: "容量估计区间明显偏移", on: false },
  { id: "collect_fail", label: "采集失败", desc: "脱敏后的失败类型与影响范围", on: true },
  { id: "temp_full", label: "临时轮次接近用满", desc: "需在每个轮次单独开启", on: true },
];

export const notifyLog = [
  { id: "n-771", at: "2026-11-04 14:16", kind: "collect_fail", subject: "lab-east 采集失败（token 刷新）", to: "ops@example.org", participant: null, outcome: "success", reason: "" },
  { id: "n-770", at: "2026-11-04 14:01", kind: "collect_fail", subject: "lab-east 采集失败（token 刷新）", to: "ops@example.org", participant: null, outcome: "skipped", reason: "重复提醒抑制：同一问题 60 分钟内已通知" },
  { id: "n-768", at: "2026-11-04 12:30", kind: "low_balance", subject: "周承 余额 6.10，建议补充至 15.50", to: "ops@example.org", participant: "m2", outcome: "success", reason: "" },
  { id: "n-765", at: "2026-11-04 08:00", kind: "rec_change", subject: "林雨 建议余额变化 22.50 → 24.00", to: "ops@example.org", participant: "m1", outcome: "failed", reason: "SMTP 连接超时" },
  { id: "n-760", at: "2026-11-03 19:02", kind: "temp_full", subject: "R-07 已开启用满提醒", to: "ops@example.org", participant: null, outcome: "success", reason: "" },
];

/* ---------------- 安全 ---------------- */

export const loginAudit = [
  { at: "2026-11-04 14:02", user: "admin", ok: true, ip: "10.20.3.14", hint: null, reason: "" },
  { at: "2026-11-04 13:40", user: "lin.yu", ok: true, ip: "10.20.5.88", hint: "192.168.1.23（浏览器自报，不可信）", reason: "" },
  { at: "2026-11-04 12:11", user: "admin", ok: false, ip: "203.0.113.45", hint: null, reason: "密码错误" },
  { at: "2026-11-04 12:10", user: "admin", ok: false, ip: "203.0.113.45", hint: null, reason: "密码错误" },
  { at: "2026-11-04 12:09", user: "root", ok: false, ip: "203.0.113.45", hint: null, reason: "用户不存在" },
  { at: "2026-11-04 12:09", user: "admin", ok: false, ip: "203.0.113.45", hint: null, reason: "来源已阻断" },
];

export const ipBlocks = [
  { ip: "203.0.113.45", at: "2026-11-04 12:12", by: "自动（连续失败）", until: "2026-11-05 12:12", reason: "5 分钟内失败 4 次" },
  { ip: "198.51.100.0/24", at: "2026-10-20 09:00", by: "admin", until: "永久", reason: "已知扫描网段" },
];

export const adminAudit = [
  { at: "2026-11-04 13:00", actor: "自动任务", action: "余额应用", target: "周承 (u_3027)", result: "unknown" },
  { at: "2026-11-04 11:50", actor: "admin", action: "整账号暂停", target: "studio-north 至 16:00", result: "confirmed" },
  { at: "2026-11-04 10:20", actor: "admin", action: "计价变更 PC-14", target: "openai-team, shared-misc", result: "partial" },
  { at: "2026-11-03 19:00", actor: "admin", action: "开启临时轮次 R-07（结转）", target: "Alpha 共享池", result: "confirmed" },
  { at: "2026-11-02 16:30", actor: "admin", action: "修改用户权限", target: "lin.yu", result: "confirmed" },
  { at: "2026-11-01 08:00", actor: "admin", action: "历史重建 MP-03", target: "Alpha 主账号 10-01 → 10-31", result: "confirmed" },
];

/* ---------------- 系统用户 ---------------- */

export const FEATURES = [
  { id: "overview", label: "概览" },
  { id: "accounts", label: "账号与额度" },
  { id: "usage", label: "用量与费用" },
  { id: "participants", label: "参与者与池" },
  { id: "observations", label: "观测记录" },
  { id: "estimates", label: "估算" },
  { id: "balances", label: "余额建议" },
  { id: "notifications", label: "通知记录" },
  { id: "dispatch", label: "账号暂停状态" },
  { id: "audit", label: "登录审计" },
];

export const sysUsers = [
  { id: "su1", username: "admin", role: "admin", status: "active", features: FEATURES.map((f) => f.id), accounts: ["*"], participants: ["*"], lastLogin: "2026-11-04 14:02" },
  { id: "su2", username: "lin.yu", role: "user", status: "active", features: ["overview", "accounts", "usage", "balances", "notifications"], accounts: ["a1", "a2"], participants: ["m1"], lastLogin: "2026-11-04 13:40" },
  { id: "su3", username: "finance", role: "user", status: "active", features: ["overview", "usage", "balances", "participants"], accounts: ["a1", "a2", "a3", "a4"], participants: ["m1", "m2", "m3", "m4"], lastLogin: "2026-11-01 10:22" },
  { id: "su4", username: "kai.m", role: "user", status: "disabled", features: ["overview", "usage"], accounts: ["a3"], participants: ["m5"], lastLogin: "2026-10-30 18:00" },
];

/* ---------------- API Key ---------------- */

export const apiKeys = [
  { id: "ak_01", name: "ops-bot", owner: "admin", scope: "管理写：应用余额建议", prefix: "spk_live_3f9a", created: "2026-10-12", lastUsed: "2026-11-03 21:00", status: "active" },
  { id: "ak_02", name: "grafana-reader", owner: "admin", scope: "只读查询", prefix: "spk_live_81cc", created: "2026-09-30", lastUsed: "2026-11-04 14:10", status: "active" },
  { id: "ak_03", name: "lin 个人脚本", owner: "lin.yu", scope: "只读查询（本人范围）", prefix: "spk_live_c07e", created: "2026-10-20", lastUsed: "2026-11-02 09:00", status: "active" },
  { id: "ak_04", name: "旧导出脚本", owner: "admin", scope: "只读查询", prefix: "spk_live_0b12", created: "2026-08-01", lastUsed: "2026-09-15 10:00", status: "revoked" },
];

export const apiCatalog = [
  { group: "账号", name: "账号状态与额度窗口", method: "query", write: false },
  { group: "看板", name: "概览数据", method: "query", write: false },
  { group: "参与者", name: "参与者与份额", method: "query", write: false },
  { group: "观测", name: "观测记录", method: "query", write: false },
  { group: "统计", name: "用量与费用统计", method: "query", write: false },
  { group: "余额", name: "建议与应用状态", method: "query", write: false },
  { group: "余额", name: "应用合法正数建议", method: "mutation", write: true },
  { group: "通知", name: "通知记录", method: "query", write: false },
];

/* ---------------- 维护 ---------------- */

export const maintPlans = [
  { id: "MP-05", created: "2026-11-04 10:00", scope: "北区工作室 10-01 → 11-03", kind: "派生结果重建", status: "ready", issues: 2, blockers: 0, basisAt: "2026-11-04 10:00", rules: "估算规则 v2" },
  { id: "MP-04", created: "2026-11-02 15:00", scope: "全部账号 10 月", kind: "周期分段检查", status: "expired", issues: 5, blockers: 1, basisAt: "2026-11-02 15:00", rules: "估算规则 v2", expiredReason: "11-04 排除了 ob-88399，依据已变化" },
  { id: "MP-03", created: "2026-11-01 07:40", scope: "Alpha 主账号 10-01 → 10-31", kind: "派生结果重建", status: "applied", issues: 1, blockers: 0, basisAt: "2026-11-01 07:40", rules: "估算规则 v2" },
  { id: "MP-02", created: "2026-10-28 11:00", scope: "东区实验 10 月", kind: "规则升级重算", status: "failed", issues: 3, blockers: 0, basisAt: "2026-10-28 11:00", rules: "估算规则 v1 → v2" },
];

export const maintIssues = [
  { kind: "周期分段", desc: "10-12 09:00 本地分段与官方重置不一致（官方无重置证据）", fixable: true },
  { kind: "观测缺口", desc: "10-30 全天无观测，重建保持缺口，不补造样本", fixable: false },
];

/* ---------------- 备份 ---------------- */

export const backups = [
  { id: "bk-20261104-0300", at: "2026-11-04 03:00", by: "定时", size: "18.4 MB", schema: "v12", status: "success", secrets: "已加密" },
  { id: "bk-20261103-0300", at: "2026-11-03 03:00", by: "定时", size: "18.1 MB", schema: "v12", status: "success", secrets: "已加密" },
  { id: "bk-20261101-1530", at: "2026-11-01 15:30", by: "admin", size: "17.6 MB", schema: "v11", status: "success", secrets: "已排除" },
];

/* ---------------- 公告与教程 ---------------- */

export const announcements = [
  {
    id: "an-6",
    date: "2026-11-02",
    title: "余额应用新增“结果未知”状态",
    body: "当上游写入超时且无法读回时，记录保持“结果未知”，需人工核对后才可进行下一次应用。此前版本会将此类情况显示为失败。",
  },
  {
    id: "an-5",
    date: "2026-10-25",
    title: "临时放宽支持“不结转”模式",
    body: "开启不结转轮次时，需确认已告知参与者。该勾选只记录业务确认，系统不会因此逐人发送邮件。",
  },
  {
    id: "an-4",
    date: "2026-10-12",
    title: "参与者份额历史可追溯",
    body: "每次份额修改都会生成新版本，历史观测保留当时使用的份额依据，不会被新方案改写。",
  },
];

export const tutorials = [
  { id: "t-conn", title: "连接 Sub2API 并核对能力", minutes: 4, roles: "管理员", body: ["在“连接与采集”中填写服务地址与管理凭据，凭据保存后只显示“已配置”。", "点击“测试连接”。健康响应只代表服务可达；能力列表会逐项显示可用、缺权限、不支持或未验证。", "运行模式无法确认时，依赖钱包扣费和计价写入的功能保持关闭。"] },
  { id: "t-pool", title: "建立额度池与份额", minutes: 5, roles: "管理员", body: ["先在“参与者”中绑定明确的上游用户，系统不会按同名自动绑定。", "在额度池中加入纳管账号，再分配份额。合计不能超过 100%，未分配部分不会自动归给任何人。", "修改份额只影响之后的新观测，历史观测保留当时的份额依据。"] },
  { id: "t-est", title: "读懂估算与“未知”", minutes: 3, roles: "全部用户", body: ["估算是派生结果，带有范围、前提和数据时间，不等于官方额度。", "数据不足、来源过期或前提变化时，界面显示原因而非 0。", "描述性统计（每日收盘、月汇总）与模型估计分开展示。"] },
  { id: "t-bal", title: "余额建议与应用", minutes: 5, roles: "管理员", body: ["只有已验证的正数建议、且钱包整体管理范围已确认时，才允许应用。", "应用结果分为已确认、失败、结果未知。结果未知需人工核对，不会被当作失败重试。", "自动应用默认关闭；开启后仍会跳过混用钱包、活动冻结和零目标。"] },
  { id: "t-temp", title: "临时放宽的开启与结束", minutes: 6, roles: "管理员", body: ["开启前系统会核验数据新鲜度、绑定、周期和运行模式。", "首个相关账号换周期或最早截止到达时，全局恢复普通建议；其余账号的原周期分别处理。", "“普通建议已恢复”不等于“远端高余额已收回”，后者需单独核对。"] },
  { id: "t-restore", title: "备份与恢复", minutes: 3, roles: "管理员", body: ["备份只包含本系统业务数据，不包含 Sub2API 数据库。", "恢复前会预检兼容性与运行中操作；恢复后需重新登录，并核对连接和未完成任务。", "本地回滚不会撤销已经发生的上游写入。"] },
];

/* ---------------- helpers ---------------- */

export const accountName = (id: string) => accounts.find((a) => a.id === id)?.localName ?? id;
export const participantName = (id: string) => participants.find((p) => p.id === id)?.name ?? id;
export const poolName = (id: string | null) => (id ? pools.find((p) => p.id === id)?.name ?? id : "未入池");
