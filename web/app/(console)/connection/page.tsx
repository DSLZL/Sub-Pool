"use client";

import { useState } from "react";
import { Plug, ShieldCheck, Search, Plus } from "lucide-react";
import {
  Badge,
  Button,
  Callout,
  Card,
  CapBadge,
  ConfirmModal,
  Field,
  Grid,
  Input,
  KV,
  Modal,
  Mono,
  PageHeader,
  Select,
  Switch,
  Table,
  Tabs,
  Outcome,
} from "@/components/ui";
import { useApp } from "@/lib/store";
import { accounts as allAccounts, capabilities, connection, discovered, jobs, TZ } from "@/lib/mock";

type TestResult = { step: string; outcome: string; detail: string }[];

export default function ConnectionPage() {
  const { toast } = useApp();
  const [tab, setTab] = useState("conn");
  const [url, setUrl] = useState(connection.baseUrl);
  const [newSecret, setNewSecret] = useState("");
  const [credSet, setCredSet] = useState(connection.credentialSet);
  const [testing, setTesting] = useState(false);
  const [test, setTest] = useState<TestResult | null>(null);
  const [urlConfirm, setUrlConfirm] = useState(false);
  const [clearConfirm, setClearConfirm] = useState(false);
  const urlChanged = url !== connection.baseUrl;

  const [accs, setAccs] = useState(allAccounts.map((a) => ({ id: a.id, upstreamId: a.upstreamId, name: a.localName, monitoring: a.monitoring, source: a.sourceMode })));
  const [q, setQ] = useState("");
  const [addOpen, setAddOpen] = useState<null | (typeof discovered)[number]>(null);
  const [addName, setAddName] = useState("");

  const [job, setJob] = useState({ enabled: jobs.enabled, interval: String(jobs.intervalMin), boost: jobs.nearResetBoost, stale: String(jobs.staleAfterMin) });
  const [tz, setTz] = useState(TZ);

  const runTest = () => {
    setTesting(true);
    setTest(null);
    setTimeout(() => {
      setTesting(false);
      const unreachable = url.includes("invalid");
      setTest(
        unreachable
          ? [{ step: "网络可达", outcome: "failed", detail: "连接超时（10s）。请检查地址、网络或代理设置。" }]
          : [
              { step: "网络可达", outcome: "success", detail: "182 ms" },
              { step: "管理凭据鉴权", outcome: "success", detail: "凭据有效" },
              { step: "合规确认", outcome: "success", detail: "未返回 423" },
              { step: "读取账号列表", outcome: "success", detail: "可见 7 个账号" },
              { step: "读取模型配置", outcome: "failed", detail: "403：当前凭据缺少该权限" },
              { step: "识别运行模式", outcome: "success", detail: "standard（来自 auth/me）" },
            ],
      );
    }, 1100);
  };

  return (
    <>
      <PageHeader title="连接与采集" tag="S2A-CONN · S2A-ACC-001/002 · S2A-JOB-001/002 · S2A-OPS-004" desc="本系统只接入一个 Sub2API 实例。连接测试、能力检查和账号纳管都以实际返回为准。" />
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "conn", label: "Sub2API 连接" },
          { id: "cap", label: "能力检查" },
          { id: "acc", label: "账号纳管" },
          { id: "job", label: "采集设置" },
          { id: "sys", label: "系统设置" },
        ]}
      />

      {tab === "conn" && (
        <Grid cols={2}>
          <Card title="连接配置" desc={`最近保存 ${connection.savedAt}`}>
            <div className="space-y-4">
              <Field label="服务地址" hint="只支持单一 Sub2API 实例，不提供平台切换。">
                <Input value={url} onChange={(e) => setUrl(e.target.value)} />
              </Field>
              {urlChanged && (
                <Callout tone="warn" title="目标地址将变更">
                  更换后，现有账号映射、未完成的余额写入和本系统托管的计价修改都需要重新核对。同名或同 ID 不代表同一资源，旧写入不会被迁移或重放。
                </Callout>
              )}
              <Field label="管理凭据" hint="保存后不再显示原文，只显示是否已配置。">
                <div className="mb-2 flex items-center gap-2">
                  {credSet ? <Badge tone="ok">{connection.credentialHint}</Badge> : <Badge tone="muted">未配置</Badge>}
                  {credSet && (
                    <button className="text-[12px] text-bad hover:underline" onClick={() => setClearConfirm(true)}>
                      清除
                    </button>
                  )}
                </div>
                <Input type="password" placeholder={credSet ? "输入新凭据以替换" : "输入管理凭据"} value={newSecret} onChange={(e) => setNewSecret(e.target.value)} autoComplete="off" />
              </Field>
              <div className="flex flex-wrap gap-2">
                <Button icon={<Plug size={14} />} onClick={runTest} disabled={testing}>
                  {testing ? "测试中…" : "测试连接"}
                </Button>
                <Button
                  variant="primary"
                  onClick={() => {
                    if (urlChanged) setUrlConfirm(true);
                    else {
                      if (newSecret) setCredSet(true);
                      setNewSecret("");
                      toast({ tone: "ok", title: "连接配置已保存" });
                    }
                  }}
                >
                  保存
                </Button>
              </div>
            </div>
          </Card>
          <Card title="测试结果" desc="可在保存前测试未保存的参数。服务可达不代表管理能力可用。">
            {!test && !testing && <p className="text-[13px] text-muted">尚未测试。最近一次：{connection.lastTest.at}，成功，{connection.lastTest.latencyMs} ms。</p>}
            {testing && <p className="text-[13px] text-muted">正在逐步检查…</p>}
            {test && (
              <ol className="space-y-2.5">
                {test.map((t) => (
                  <li key={t.step} className="flex items-start gap-3">
                    <Outcome k={t.outcome} />
                    <div>
                      <div className="text-[13px] font-medium">{t.step}</div>
                      <div className="text-[12px] text-muted">{t.detail}</div>
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </Card>
        </Grid>
      )}

      {tab === "cap" && (
        <Card
          title="逐功能能力状态"
          desc={`实例版本 ${connection.version} · 运行模式 ${connection.runMode ?? "未知"} · 不因账号列表成功推断其他接口可用`}
          actions={
            <Button size="sm" icon={<ShieldCheck size={13} />} onClick={() => toast({ tone: "info", title: "已重新检查能力", body: "结果与上次一致。" })}>
              重新检查
            </Button>
          }
          pad={false}
        >
          <Table
            rows={capabilities}
            rowKey={(c) => c.id}
            cols={[
              { key: "n", label: "能力", render: (c) => <div><div className="font-medium">{c.name}</div><div className="text-[12px] text-muted">{c.desc}</div></div> },
              { key: "s", label: "状态", render: (c) => <CapBadge c={c.state} /> },
              { key: "note", label: "说明", render: (c) => <span className="text-[12.5px] text-muted">{c.note}</span> },
            ]}
          />
          <div className="border-t border-line p-4">
            <Callout tone="info">运行模式无法确认时，依赖钱包扣费或计价写入的功能会被禁用。本系统不会自动提升权限或修改上游授权。</Callout>
          </div>
        </Card>
      )}

      {tab === "acc" && (
        <div className="space-y-4">
          <Card title="已纳管账号" pad={false}>
            <Table
              rows={accs}
              rowKey={(a) => a.id}
              cols={[
                { key: "n", label: "本地名称", render: (a) => <span className="font-medium">{a.name}</span> },
                { key: "u", label: "上游 ID", render: (a) => <Mono>{a.upstreamId}</Mono> },
                {
                  key: "src",
                  label: "额度来源",
                  render: (a) => (
                    <Select
                      className="w-32"
                      value={a.source}
                      onChange={(e) => setAccs((xs) => xs.map((x) => (x.id === a.id ? { ...x, source: e.target.value as "active" | "passive" } : x)))}
                      options={[
                        { value: "active", label: "主动查询" },
                        { value: "passive", label: "被动快照" },
                      ]}
                    />
                  ),
                },
                {
                  key: "m",
                  label: "本地监控",
                  render: (a) => (
                    <Switch
                      checked={a.monitoring}
                      onChange={(v) => {
                        setAccs((xs) => xs.map((x) => (x.id === a.id ? { ...x, monitoring: v } : x)));
                        toast({
                          tone: v ? "ok" : "warn",
                          title: v ? `已恢复监控：${a.name}` : `已暂停监控：${a.name}`,
                          body: v ? "下次采集将按计划执行。" : "历史保留；这不会关闭上游调度，也不会收回已写入的余额。",
                        });
                      }}
                    />
                  ),
                },
              ]}
            />
          </Card>
          <Card title="发现可纳管账号" desc="读取上游账号列表；读取不到不等于已删除。" pad={false}>
            <div className="p-4 pb-0">
              <div className="relative max-w-xs">
                <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
                <Input className="pl-8" placeholder="按名称或 ID 筛选" value={q} onChange={(e) => setQ(e.target.value)} />
              </div>
            </div>
            <div className="pt-3">
              <Table
                rows={discovered.filter((d) => !q || d.name.includes(q) || d.upstreamId.includes(q))}
                rowKey={(d) => d.upstreamId}
                cols={[
                  { key: "n", label: "上游账号", render: (d) => <div><div>{d.name}</div><Mono>{d.upstreamId}</Mono></div> },
                  { key: "t", label: "类型", render: (d) => d.type },
                  { key: "s", label: "上游状态", render: (d) => <Badge tone={d.status === "启用" ? "ok" : d.status === "异常" ? "bad" : "muted"}>{d.status}</Badge> },
                  {
                    key: "a",
                    label: "",
                    render: (d) =>
                      d.managed ? (
                        <Badge tone="brand">已纳管</Badge>
                      ) : d.reason ? (
                        <span className="text-[12px] text-warn">{d.reason}</span>
                      ) : (
                        <Button
                          size="sm"
                          icon={<Plus size={13} />}
                          onClick={() => {
                            setAddOpen(d);
                            setAddName(d.name);
                          }}
                        >
                          纳管
                        </Button>
                      ),
                  },
                ]}
              />
            </div>
          </Card>
        </div>
      )}

      {tab === "job" && (
        <Grid cols={2}>
          <Card title="采集安排">
            <div className="space-y-4">
              <Switch checked={job.enabled} onChange={(v) => setJob({ ...job, enabled: v })} label="启用定时采集" desc="关闭后不会自动收回远端余额；临时模式的恢复义务仍需单独处理。" />
              <Field label="常规频率（分钟）">
                <Select value={job.interval} onChange={(e) => setJob({ ...job, interval: e.target.value })} options={["5", "10", "15", "30", "60"]} />
              </Field>
              <Switch checked={job.boost} onChange={(v) => setJob({ ...job, boost: v })} label="临近重置时加密观察" desc="在窗口重置前后提高采集频率。" />
              <Field label="数据过旧提醒阈值（分钟）">
                <Input type="number" min={10} value={job.stale} onChange={(e) => setJob({ ...job, stale: e.target.value })} />
              </Field>
              <Button variant="primary" onClick={() => toast({ tone: "ok", title: "采集设置已保存", body: `下次执行：${jobs.nextRun}` })}>
                保存
              </Button>
            </div>
          </Card>
          <Card title="当前生效">
            <KV
              cols={1}
              items={[
                ["状态", job.enabled ? <Badge tone="ok" dot>运行中</Badge> : <Badge tone="muted" dot>已暂停</Badge>],
                ["下次执行", <span key="n" className="num">{jobs.nextRun}</span>],
                ["最近成功", <span key="s" className="num">{jobs.lastSuccess}</span>],
                ["最近一次", `${jobs.lastRun.at} · 成功 ${jobs.lastRun.ok} / 失败 ${jobs.lastRun.failed} · 用时 ${jobs.lastRun.durationS}s`],
              ]}
            />
          </Card>
        </Grid>
      )}

      {tab === "sys" && (
        <Card title="统计时区" desc="改变显示与统计时区不会改变原始时刻或上游重置时间。" className="max-w-xl">
          <div className="space-y-4">
            <Field label="时区">
              <Select value={tz} onChange={(e) => setTz(e.target.value)} options={["Asia/Shanghai", "Asia/Tokyo", "UTC", "Europe/Berlin", "America/Los_Angeles"]} />
            </Field>
            <Button variant="primary" onClick={() => toast({ tone: "ok", title: "设置已保存", body: `统计时区：${tz}` })}>
              保存
            </Button>
          </div>
        </Card>
      )}

      <ConfirmModal
        open={urlConfirm}
        onClose={() => setUrlConfirm(false)}
        onConfirm={() => toast({ tone: "warn", title: "目标地址已变更", body: "请在“账号纳管”中重新核对账号映射。" })}
        title="确认更换 Sub2API 地址"
        phrase="更换地址"
        confirmLabel="确认更换"
      >
        <p>新地址：<Mono>{url}</Mono></p>
        <ul className="list-disc space-y-1 pl-5 text-muted">
          <li>4 个已纳管账号的映射需重新核对</li>
          <li>1 项结果未知的余额写入将保持待核对，不会在新目标重放</li>
          <li>1 项本系统托管的计价修改（PC-12）将无法撤回</li>
        </ul>
      </ConfirmModal>
      <ConfirmModal
        open={clearConfirm}
        onClose={() => setClearConfirm(false)}
        onConfirm={() => {
          setCredSet(false);
          toast({ tone: "warn", title: "管理凭据已清除", body: "所有上游读写将不可用，直到重新配置。" });
        }}
        title="清除管理凭据"
        danger
        confirmLabel="清除"
      >
        清除后采集与所有上游操作立即停止。
      </ConfirmModal>
      <Modal
        open={!!addOpen}
        onClose={() => setAddOpen(null)}
        title="纳管上游账号"
        footer={
          <>
            <Button onClick={() => setAddOpen(null)}>取消</Button>
            <Button
              variant="primary"
              onClick={() => {
                if (addOpen) setAccs((xs) => [...xs, { id: addOpen.upstreamId, upstreamId: addOpen.upstreamId, name: addName, monitoring: true, source: "active" }]);
                toast({ tone: "ok", title: "已纳管", body: "首次采集将在下个周期执行。" });
                setAddOpen(null);
              }}
            >
              纳管
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <KV items={[["上游 ID", <Mono key="i">{addOpen?.upstreamId}</Mono>], ["类型", addOpen?.type]]} />
          <Field label="本地显示名称">
            <Input value={addName} onChange={(e) => setAddName(e.target.value)} />
          </Field>
          <Callout tone="info">本地编辑不会创建或重绑上游身份。</Callout>
        </div>
      </Modal>
    </>
  );
}
