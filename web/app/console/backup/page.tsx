"use client";

import { useEffect, useRef, useState } from "react";
import { Download, Upload, FileCheck2 } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, ConfirmModal, Grid, Mono, Outcome, PageHeader, Switch, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useBackupData } from "@/lib/use-backup-data";
import { backupBlockers, restoreReasons } from "@/lib/backup-data";
import { displayTime } from "@/lib/connection-data";

export default function BackupPage() {
  const { auth, timezone } = useApp(), view = useBackupData();
  const [incSecrets, setIncSecrets] = useState(true), [confirm, setConfirm] = useState(false);
  const [light, setLight] = useState(false), [copySaved, setCopySaved] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const plan = view.plan, checks = view.checks, restored = plan?.state === "applied" && checks !== null;
  const file = view.file?.name ?? (view.knownID ? "已保存操作 " + view.knownID : null);
  const pending = view.pending !== null;
  useEffect(() => { setConfirm(false); setCopySaved(false); }, [auth.epoch, view.allowed, plan?.id]);
  const cleared = plan?.cleared_tables ?? [];
  const clearSummary = cleared.map(table => table.name + " " + table.rows + " 行").join("、");
  const beforeDownloaded = plan?.before_archive_id != null && view.downloaded === plan.before_archive_id;
  const work = plan?.blockers.filter(code => !code.includes("REMOTE_WRITE") && code !== "BACKUP_SOURCE_BLOCKED") ?? [];
  const remote = plan?.blockers.filter(code => code.includes("REMOTE_WRITE") || code === "BACKUP_SOURCE_BLOCKED") ?? [];
  const precheckItems = plan ? [
    { k: "备份格式与完整性", outcome: "success", note: plan.source.format + " · SHA-256 " + plan.file_sha256.match(/.{1,16}/g)?.join(" ") },
    { k: "结构与范围兼容", outcome: "success", note: (plan.source.scope === "light" ? "轻量档 · " : "完整档 · ") + plan.source.tables.length + " 张业务表 · " + plan.source.rows + " 行 · 来源 " + displayTime(plan.source.source_cutoff, timezone) },
    { k: "当前与备份中的活动任务", outcome: work.length ? "conflict" : "success", note: work.length ? work.map(code => backupBlockers[code]).join("；") : "服务端预检未发现待处理的采集、邮件或维护任务；确认时再次核对。" },
    { k: "未确认的远端写入与来源访问", outcome: remote.length ? "reconcile" : "success", note: remote.length ? remote.map(code => backupBlockers[code]).join("；") : "服务端未发现未证实未派发的财务意图或会阻断当前来源的导入规则；不撤销上游写入。" },
    { k: "恢复前快照", outcome: plan.before_archive_id ? "success" : "conflict", note: (plan.before_archive_id ? (beforeDownloaded ? "已核对副本下载 " : "请在备份记录中下载 ") + plan.before_archive_id + "。 " : "阻塞的预检没有可用于确认的恢复前快照。 ") + (beforeDownloaded && plan.state === "ready" ? "请在恢复对话框确认已保存副本" : restoreReasons[plan.reason]) + " · 有效至 " + displayTime(plan.expires_at, timezone) },
    ...(cleared.length ? [{ k: "轻量恢复将清空的表", outcome: "reconcile", note: clearSummary }] : []),
  ] : [];
  const postChecks = checks ? [
    { label: "原会话已清除，当前身份已重新登录", checked: checks.sessions === "prior_sessions_removed" },
    { label: checks.connection_state === "verified_after_restore" ? "当前保存的 Sub2API 连接已通过实际管理鉴权测试" : checks.connection_state === "not_configured" ? "尚未配置 Sub2API 连接，请在连接页配置并测试" : "Sub2API 连接仍需在连接页重新测试", checked: checks.connection_state === "verified_after_restore" },
    { label: "当前有效 API Key：" + checks.active_api_keys + "；恢复时导入的密钥已撤销", checked: checks.active_api_keys === "0" },
    { label: "定时采集与邮件当前均保持暂停；人工勾选不会恢复任务", checked: checks.monitoring_paused && checks.mail_paused },
    { label: "旧远端操作未重放；未知结果仍需另行核对", checked: checks.remote_effects === "not_replayed" },
    { label: checks.reviewed_at ? "人工核对已记录：" + checks.reviewed_by + " · " + displayTime(checks.reviewed_at, timezone) : "已人工核对账号映射、参与者绑定、临时轮次与未知结果", checked: checks.reviewed_at !== null, manual: true },
  ] : [];

  return (
    <>
      <PageHeader
        title="备份与恢复"
        desc="备份只包含本系统（PostgreSQL）的业务数据，不包含 Sub2API 数据库。本地恢复不会撤销已经发生的上游写入。"
      />
      <Grid cols={2}>
        <Card title="导出备份">
          <Switch checked={incSecrets} onChange={value => { if (!pending && !view.uncertain) setIncSecrets(value); }} label="包含加密后的秘密" desc="关闭则排除 Sub2API 凭据与邮件秘密，恢复后需重新配置。" />
          <Switch checked={light} onChange={value => { if (!pending && !view.uncertain) setLight(value); }} label="轻量备份" desc="仅保留配置、账号、授权、绑定与池关系。恢复前会列出将清空的历史数据。" />
          <Button
            variant="primary"
            className="mt-4"
            icon={<Download size={14} />}
            disabled={!view.allowed || pending || (view.archives === null || !view.keyAvailable) && !view.uncertainExport || view.uncertain && !view.uncertainExport}
            onClick={() => { void view.createExport(incSecrets, light ? "light" : "full"); }}
          >
            {view.uncertainExport ? "核对导出结果" : view.pending === "export" ? "正在备份…" : "立即备份"}
          </Button>
        </Card>
        <Card title="从备份恢复">
          <input
            ref={inputRef}
            type="file"
            accept=".spbak"
            className="hidden"
            disabled={pending || view.uncertain}
            onChange={event => { const selected = event.target.files?.[0]; event.target.value = ""; if (selected) void view.choose(selected); }}
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button icon={<Upload size={14} />} disabled={!view.allowed || pending || view.uncertain} onClick={() => inputRef.current?.click()}>
              选择备份文件
            </Button>
            <Button disabled={!view.allowed || pending || view.archives?.length === 0 && !view.knownID && !view.uncertain} onClick={() => { void view.latest(); }}>
              {view.knownID || view.uncertain || view.archives === null ? "刷新并核对记录" : "使用最近一次备份"}
            </Button>
          </div>
          {file && (
            <div className="mt-3 flex items-center gap-2 text-[13px]">
              <FileCheck2 size={14} className="text-muted" /> <Mono>{file}</Mono>
              <Button size="sm" disabled={pending || !view.allowed || (view.uncertain || !view.file ? !view.knownID : view.archives === null || !view.keyAvailable)} onClick={() => { void view.preflight(); }}>
                {view.uncertain || !view.file ? "核对预检" : "预检"}
              </Button>
            </div>
          )}
          <p className="mt-3 text-[12px] text-muted">{view.notice?.text ?? (pending ? "正在读取并核对备份数据…" : view.keyAvailable === false ? "服务未配置保护密钥。已有文件仍可下载。" : "请选择本系统的 .spbak 文件后预检。")}</p>
        </Card>
      </Grid>

      {plan && !restored && (
        <Card title="恢复预检" className="mt-4" actions={<Button variant="danger" disabled={!view.canApply} onClick={() => setConfirm(true)}>恢复</Button>}>
          <ul className="space-y-2.5">
            {precheckItems.map(p => (
              <li key={p.k} className="flex items-start gap-3 text-[13px]">
                <Outcome k={p.outcome} />
                <div>
                  <div className="font-medium">{p.k}</div>
                  <div className="text-muted">{p.note}</div>
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {restored && checks && (
        <Card title="恢复后业务核对" className="mt-4" desc="服务端状态与人工核对分别记录；不会自动恢复采集、邮件或远端操作">
          <Callout tone="ok" className="mb-4">服务端已完成本地恢复。来源快照：{displayTime(checks.source.source_cutoff, timezone)} · {checks.source.rows} 行。上游写入不会被回滚，旧通知和旧远端操作不会重放。</Callout>
          <div className="space-y-2">
            {postChecks.map(c => (
              <Checkbox key={c.label} checked={c.checked} disabled={!c.manual || c.checked || pending || view.uncertain} onChange={value => { if (value && c.manual) void view.review(); }} label={c.label} />
            ))}
          </div>
          <div className="mt-4 text-[12px] text-muted">
            已确认 {postChecks.filter(c => c.checked).length} / {postChecks.length}
          </div>
        </Card>
      )}

      <Card title="备份记录" className="mt-4" pad={false}>
        <Table
          rows={view.archives ?? []}
          rowKey={b => b.id}
          empty={view.archives === null ? pending ? "正在读取备份记录…" : "备份记录尚未确认，请刷新并核对。" : "尚无已保存的备份"}
          cols={[
            { key: "id", label: "备份", render: b => <Mono>{b.id}</Mono> },
            { key: "t", label: "时间", render: b => <span className="num">{displayTime(b.created_at, timezone)}</span> },
            { key: "by", label: "触发", render: b => b.actor_username + (b.kind === "pre_restore" ? " · 恢复前" : b.scope === "light" ? " · 轻量" : " · 完整") },
            { key: "s", label: "大小", render: b => new Intl.NumberFormat("zh-CN").format(BigInt(b.bytes)) + " B" },
            { key: "v", label: "结构标识", render: b => <span title={b.schema_fingerprint}><Mono>{b.schema_fingerprint.slice(0, 12)}</Mono></span> },
            { key: "sec", label: "秘密", render: b => <Badge tone={b.include_secrets ? "ok" : "muted"}>{b.include_secrets ? "已加密" : "已排除"}</Badge> },
            { key: "st", label: "结果", render: () => <Outcome k="success" /> },
            { key: "d", label: "", render: b => <Button size="sm" variant="ghost" disabled={pending || !view.allowed} icon={<Download size={12} />} onClick={() => { void view.download(b); }}>下载</Button> },
          ]}
        />
      </Card>

      <ConfirmModal
        open={confirm && view.allowed}
        onClose={() => { setConfirm(false); setCopySaved(false); }}
        onConfirm={view.apply}
        disabled={!view.canApply || !!plan?.source_bytes && !copySaved}
        title="恢复本系统备份"
        phrase="恢复备份"
        confirmLabel="开始恢复"
        danger
      >
        <ul className="list-disc space-y-1 pl-5">
          <li>当前业务数据将被替换，已保留并下载真实恢复前快照</li>
          {cleared.length > 0 && <li><details open><summary>将清空的 {cleared.length} 张表</summary><div className="max-h-32 overflow-auto">{clearSummary}</div></details></li>}
          <li>恢复后采集与邮件保持暂停，需另行恢复</li>
          <li>将清除所有会话并撤销导入的 API Key；须使用备份中的管理员重新登录</li>
          <li>{view.notice?.unknown ? view.notice.text : "已发生的上游写入仍然有效"}</li>
        </ul>
        {plan?.source_bytes && <Checkbox checked={copySaved} onChange={setCopySaved} label="我已将完整的恢复前副本保存到本机" />}
      </ConfirmModal>
    </>
  );
}
