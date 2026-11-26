"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { useEffect, useRef, useState } from "react";
import { Download, Upload, FileCheck2 } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, ConfirmModal, Grid, Mono, Outcome, PageHeader, Switch, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useBackupData } from "@/lib/use-backup-data";
import { backupBlockers, restoreReasons } from "@/lib/backup-data";
import { displayTime } from "@/lib/connection-data";

export default function BackupPage() {
  const { auth, displayTimezone } = useApp(), view = useBackupData();
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
    { k: "格式与校验", outcome: "success", note: plan.source.format + " · SHA-256 " + plan.file_sha256.match(/.{1,16}/g)?.join(" ") },
    { k: "兼容性", outcome: "success", note: (plan.source.scope === "light" ? "轻量档 · " : "完整档 · ") + plan.source.tables.length + " 张业务表 · " + plan.source.rows + " 行 · 来源 " + displayTime(plan.source.source_cutoff, displayTimezone, false) },
    { k: "活动任务", outcome: work.length ? "conflict" : "success", note: work.length ? work.map(code => backupBlockers[code]).join("；") : "预检通过。" },
    { k: "未决操作", outcome: remote.length ? "reconcile" : "success", note: remote.length ? remote.map(code => backupBlockers[code]).join("；") : "无" },
    { k: "恢复前快照", outcome: plan.before_archive_id ? "success" : "conflict", note: (plan.before_archive_id ? (beforeDownloaded ? "已核对下载" : "下载副本") + plan.before_archive_id + "。 " : "预检有阻碍。") + (beforeDownloaded && plan.state === "ready" ? "确认保存副本" : restoreReasons[plan.reason]) + " · 有效至 " + displayTime(plan.expires_at, displayTimezone, false) },
    ...(cleared.length ? [{ k: "清除项", outcome: "reconcile", note: clearSummary }] : []),
  ] : [];
  const postChecks = checks ? [
    { label: "已重新登录", checked: checks.sessions === "prior_sessions_removed" },
    { label: checks.connection_state === "verified_after_restore" ? `当前保存的 ${glossary.sub2api} 连接已验证` : checks.connection_state === "not_configured" ? `未配置 ${glossary.sub2api}，请配置连接` : `${glossary.sub2api} 需重测连接`, checked: checks.connection_state === "verified_after_restore" },
    { label: "当前有效 API Key：" + checks.active_api_keys + "；密钥已撤销", checked: checks.active_api_keys === "0" },
    { label: "采集和邮件暂停", checked: checks.monitoring_paused && checks.mail_paused },
    { label: "待核对旧操作", checked: checks.remote_effects === "not_replayed" },
    { label: checks.reviewed_at ? "已核对：" + checks.reviewed_by + " · " + displayTime(checks.reviewed_at, displayTimezone, false) : "已核对", checked: checks.reviewed_at !== null, manual: true },
  ] : [];

  return (
    <>
      <PageHeader
        title={uiCopy.backups}
        helpTopic="recovery"
      />
      <Grid cols={2}>
        <Card title="导出备份">
          <Switch checked={incSecrets} onChange={value => { if (!pending && !view.uncertain) setIncSecrets(value); }} label="含加密凭据" desc={`关闭则排除 ${glossary.sub2api} 凭据。`} />
          <Switch checked={light} onChange={value => { if (!pending && !view.uncertain) setLight(value); }} label="轻量备份" desc="配置及关系。" />
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
              {view.knownID || view.uncertain || view.archives === null ? "刷新记录" : "用上次备份"}
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
          <p className="mt-3 text-[12px] text-muted">{view.notice?.text ?? (pending ? "读取备份…" : view.keyAvailable === false ? "缺少保护密钥。" : "选择 .spbak 文件。")}</p>
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
        <Card title="恢复核对" className="mt-4">
          <Callout tone="ok" className="mb-4">已恢复 {displayTime(checks.source.source_cutoff, displayTimezone, false)} 的快照 · {checks.source.rows} 行</Callout>
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
          empty={view.archives === null ? pending ? "读取备份…" : uiCopy.pendingResult : "暂无备份"}
          cols={[
            { key: "id", label: "备份", render: b => <Mono>{b.id}</Mono> },
            { key: "t", label: "时间", render: b => <Timestamp short className="num" value={b.created_at} /> },
            { key: "by", label: "触发", render: b => b.actor_username + (b.kind === "pre_restore" ? " · 恢复前" : b.scope === "light" ? " · 轻量" : " · 完整") },
            { key: "s", label: "大小", render: b => new Intl.NumberFormat("zh-CN").format(BigInt(b.bytes)) + " B" },
            { key: "v", label: "结构标识", render: b => <span title={b.schema_fingerprint}><Mono>{b.schema_fingerprint.slice(0, 12)}</Mono></span> },
            { key: "sec", label: "秘密", render: b => <Badge tone={b.include_secrets ? "ok" : "muted"}>{b.include_secrets ? "已加密" : "已排除"}</Badge> },
            { key: "st", label: uiCopy.result, render: () => <Outcome k="success" /> },
            { key: "d", label: "", render: b => <Button size="sm" variant="ghost" disabled={pending || !view.allowed} icon={<Download size={12} />} onClick={() => { void view.download(b); }}>下载</Button> },
          ]}
        />
      </Card>

      <ConfirmModal
        open={confirm && view.allowed}
        onClose={() => { setConfirm(false); setCopySaved(false); }}
        onConfirm={view.apply}
        disabled={!view.canApply || !!plan?.source_bytes && !copySaved}
        title="恢复备份"
        phrase="恢复备份"
        confirmLabel="开始恢复"
        danger
      >
        <ul className="list-disc space-y-1 pl-5">
          <li>覆盖本地业务数据</li>
          {cleared.length > 0 && <li><details open><summary>将清空的 {cleared.length} 张表</summary><div className="max-h-32 overflow-auto">{clearSummary}</div></details></li>}
          <li>暂停采集和邮件</li>
          <li>会话和导入 Key 失效</li>
          <li>{view.notice?.unknown ? view.notice.text : `已发生的 ${glossary.sub2api} 写入仍然有效`}</li>
        </ul>
        {plan?.source_bytes && <Checkbox checked={copySaved} onChange={setCopySaved} label="已保存副本" />}
      </ConfirmModal>
    </>
  );
}
