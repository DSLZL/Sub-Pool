"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { RefreshCw } from "lucide-react";
import {
  AdminButton,
  Badge,
  Callout,
  Card,
  Drawer,
  FreshTag,
  KV,
  Modal,
  PageHeader,
  Segmented,
  Table,
  Tabs,
  Outcome,
  Select,
  Field,
  Button,
  Mono,
} from "@/components/ui";
import { AccountCard, StatusBadges, WindowRow } from "@/components/quota";
import { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import { jobs, poolName, type Account } from "@/lib/mock";

function AccountsInner() {
  const { accounts, isAdmin } = useScope();
  const { toast } = useApp();
  const params = useSearchParams();
  const [view, setView] = useState("grid");
  const [sel, setSel] = useState<Account | null>(null);
  const [tab, setTab] = useState("status");
  const [runOpen, setRunOpen] = useState(false);
  const [runScope, setRunScope] = useState("all");
  const [running, setRunning] = useState(false);
  const [runResult, setRunResult] = useState<null | { account: string; r: string; note: string }[]>(null);

  useEffect(() => {
    const id = params.get("id");
    if (id) setSel(accounts.find((a) => a.id === id) ?? null);
  }, [params, accounts]);

  const run = () => {
    setRunning(true);
    setRunResult(null);
    setTimeout(() => {
      setRunning(false);
      const target = runScope === "all" ? accounts : accounts.filter((a) => a.id === runScope);
      setRunResult(
        target.map((a) => ({
          account: a.localName,
          r: a.freshness === "failed" ? "failed" : a.id === "a3" ? "skipped" : "success",
          note: a.freshness === "failed" ? "token 刷新失败（上游）" : a.id === "a3" ? "上游限流，未绕过，等待下次" : "已获取窗口与用量",
        })),
      );
      toast({ tone: "info", title: "采集请求已完成", body: "请求完成不代表每个账号都成功，请查看逐账号结果。" });
    }, 1200);
  };

  return (
    <>
      <PageHeader
        title="账号与额度"
        tag="S2A-ACC · S2A-JOB"
        desc="上游账号运行状态、额度窗口及数据新鲜度。窗口缺失不等于 0，百分比不等于金额。"
        actions={
          <>
            <Segmented
              value={view}
              onChange={setView}
              options={[
                { id: "grid", label: "卡片" },
                { id: "table", label: "列表" },
              ]}
            />
            <AdminButton variant="primary" icon={<RefreshCw size={14} />} onClick={() => setRunOpen(true)}>
              手动采集
            </AdminButton>
          </>
        }
      />

      {view === "grid" ? (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
          {accounts.map((a) => (
            <AccountCard key={a.id} a={a} onClick={() => setSel(a)} />
          ))}
        </div>
      ) : (
        <Card pad={false}>
          <Table
            rows={accounts}
            rowKey={(a) => a.id}
            onRow={setSel}
            cols={[
              { key: "n", label: "账号", render: (a) => <div><div className="font-medium">{a.localName}</div><Mono>{a.upstreamId}</Mono></div> },
              { key: "s", label: "状态", render: (a) => <StatusBadges a={a} /> },
              { key: "w1", label: "短周期", align: "right", render: (a) => (a.short.usedPct === null ? <Badge tone="unk">未知</Badge> : <span className="num">{a.short.usedPct}%</span>) },
              { key: "w2", label: "7 天", align: "right", render: (a) => (!a.week ? <span className="text-faint">无此窗口</span> : a.week.usedPct === null ? <Badge tone="unk">未知</Badge> : <span className="num">{a.week.usedPct}%</span>) },
              { key: "f", label: "新鲜度", render: (a) => <FreshTag s={a.freshness} /> },
              { key: "src", label: "来源", render: (a) => (a.sourceMode === "active" ? "主动查询" : "被动快照") },
              { key: "p", label: "额度池", render: (a) => poolName(a.pool) },
            ]}
          />
        </Card>
      )}

      <Card title="采集执行记录" className="mt-6" desc={`常规频率每 ${jobs.intervalMin} 分钟 · 最近成功 ${jobs.lastSuccess} · 下次 ${jobs.nextRun}`} pad={false}>
        <Table
          rows={jobs.history}
          rowKey={(r) => r.id}
          cols={[
            { key: "id", label: "批次", render: (r) => <Mono>{r.id}</Mono> },
            { key: "at", label: "时间", render: (r) => <span className="num">{r.at}</span> },
            { key: "t", label: "触发", render: (r) => r.trigger },
            {
              key: "r",
              label: "结果",
              render: (r) => (
                <div className="flex flex-wrap gap-1">
                  <Badge tone="ok">成功 {r.ok}</Badge>
                  {r.failed > 0 && <Badge tone="bad">失败 {r.failed}</Badge>}
                  {r.skipped > 0 && <Badge tone="muted">跳过 {r.skipped}</Badge>}
                </div>
              ),
            },
            { key: "note", label: "说明", render: (r) => <span className="text-muted">{r.note || "—"}</span> },
          ]}
        />
      </Card>

      <Drawer open={!!sel} onClose={() => setSel(null)} title={sel?.localName ?? ""}>
        {sel && (
          <>
            <Tabs
              value={tab}
              onChange={setTab}
              tabs={[
                { id: "status", label: "运行状态" },
                { id: "quota", label: "额度窗口" },
                ...(isAdmin ? [{ id: "models", label: "模型配置" }] : []),
              ]}
            />
            {tab === "status" && (
              <div className="space-y-4">
                <StatusBadges a={sel} />
                {sel.error && <Callout tone="bad" title="上游报告异常">{sel.error}。本系统不会自动清除上游错误或关闭限流。</Callout>}
                <KV
                  items={[
                    ["上游名称", <Mono key="n">{sel.name}</Mono>],
                    ["上游 ID", <Mono key="i">{sel.upstreamId}</Mono>],
                    ["套餐", sel.plan],
                    ["额度池", poolName(sel.pool)],
                    ["最近使用", sel.lastUsed ?? <Badge tone="unk">未知</Badge>],
                    ["本地监控", sel.monitoring ? "运行中" : "已暂停"],
                  ]}
                />
                <Card title="数据新鲜度">
                  <KV
                    items={[
                      ["最近获取", <span className="num" key="a">{sel.lastFetch}</span>],
                      ["最近有效源观测", sel.lastSourceAt ? <span className="num">{sel.lastSourceAt}</span> : <Badge tone="unk">无法确认源时间</Badge>],
                      ["状态", <FreshTag key="s" s={sel.freshness} />],
                      ["下次计划", sel.nextRun],
                    ]}
                  />
                  {sel.freshness !== "fresh" && (
                    <Callout tone="warn" className="mt-3">
                      依赖此账号的估算、余额建议和临时模式结算将暂停给出可应用结果，直到获得有效数据。
                    </Callout>
                  )}
                </Card>
              </div>
            )}
            {tab === "quota" && (
              <div className="space-y-4">
                <WindowRow w={sel.short} label="短周期窗口" />
                <WindowRow w={sel.week} label="7 天窗口" />
                <Callout tone="info">
                  已用比例来自上游额度窗口，不是美元余额。本系统不会主动使用重置额度或重置卡；并非每种套餐都提供 7 天窗口。
                </Callout>
              </div>
            )}
            {tab === "models" && (
              <div className="space-y-3">
                {sel.models === null ? (
                  <Callout tone="unk" title="无法读取模型配置">当前凭据缺少模型配置读取权限（403），或账号未返回映射。空结果不代表全部禁用。</Callout>
                ) : (
                  <Table
                    rows={sel.models}
                    rowKey={(m) => m.requested}
                    cols={[
                      { key: "r", label: "请求模型", render: (m) => <Mono>{m.requested}</Mono> },
                      { key: "m", label: "映射到", render: (m) => (m.mapped ? <Mono>{m.mapped}</Mono> : <Badge tone="unk">未确定</Badge>) },
                    ]}
                  />
                )}
                <Callout tone="warn">配置可读不代表单模型屏蔽可靠，本系统不提供临时禁用单个模型的操作。</Callout>
              </div>
            )}
          </>
        )}
      </Drawer>

      <Modal
        open={runOpen}
        onClose={() => {
          setRunOpen(false);
          setRunResult(null);
        }}
        title="手动执行一次采集"
        footer={
          <>
            <Button onClick={() => setRunOpen(false)}>关闭</Button>
            <Button variant="primary" onClick={run} disabled={running} icon={<RefreshCw size={14} className={running ? "animate-spin" : ""} />}>
              {running ? "采集中…" : "开始采集"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field label="采集范围">
            <Select
              value={runScope}
              onChange={(e) => setRunScope(e.target.value)}
              options={[{ value: "all", label: "全部纳管账号" }, ...accounts.map((a) => ({ value: a.id, label: a.localName }))]}
            />
          </Field>
          <Callout tone="info">手动采集不会绕过上游限流。若定时任务正在运行，本次请求会报告冲突。</Callout>
          {runResult && (
            <Table
              rows={runResult}
              rowKey={(r) => r.account}
              cols={[
                { key: "a", label: "账号", render: (r) => r.account },
                { key: "r", label: "结果", render: (r) => <Outcome k={r.r} /> },
                { key: "n", label: "说明", render: (r) => <span className="text-muted">{r.note}</span> },
              ]}
            />
          )}
        </div>
      </Modal>
    </>
  );
}

export default function AccountsPage() {
  return (
    <Suspense>
      <AccountsInner />
    </Suspense>
  );
}

