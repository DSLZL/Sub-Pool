"use client";

import { useRef, useState } from "react";
import { Download, Upload, FileCheck2 } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, ConfirmModal, Grid, Mono, Outcome, PageHeader, Switch, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { backups as initial } from "@/lib/mock";

const precheckItems = [
  { k: "备份格式与校验和", outcome: "success", note: "bk-20261104-0300 · SHA-256 一致" },
  { k: "结构版本兼容", outcome: "success", note: "v12 → 当前 v12" },
  { k: "运行中的采集任务", outcome: "conflict", note: "定时采集运行中，恢复前将自动暂停" },
  { k: "未确认的远端写入", outcome: "reconcile", note: "1 项余额应用结果未知（bo-5521），恢复后需重新核对" },
  { k: "恢复前快照", outcome: "success", note: "将自动创建当前状态快照以便回退" },
];

const postChecks = [
  "重新登录（所有会话已失效）",
  "测试 Sub2API 连接并重新检查能力",
  "核对账号映射与参与者绑定",
  "核对结果未知的余额写入 bo-5521",
  "确认临时轮次 R-07 的状态与上游一致",
  "恢复定时采集",
];

export default function BackupPage() {
  const { toast } = useApp();
  const [list, setList] = useState(initial);
  const [incSecrets, setIncSecrets] = useState(true);
  const [file, setFile] = useState<string | null>(null);
  const [prechecked, setPrechecked] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [restored, setRestored] = useState(false);
  const [done, setDone] = useState<string[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);

  return (
    <>
      <PageHeader
        title="备份与恢复"
        tag="S2A-OPS-001 ~ 003"
        desc="备份只包含本系统（PostgreSQL）的业务数据，不包含 Sub2API 数据库。本地恢复不会撤销已经发生的上游写入。"
      />
      <Grid cols={2}>
        <Card title="导出备份">
          <Switch checked={incSecrets} onChange={setIncSecrets} label="包含加密后的秘密" desc="关闭则排除 Sub2API 凭据与邮件秘密，恢复后需重新配置。" />
          <Button
            variant="primary"
            className="mt-4"
            icon={<Download size={14} />}
            onClick={() => {
              setList((l) => [{ id: "bk-20261104-1423", at: "2026-11-04 14:23", by: "admin", size: "18.4 MB", schema: "v12", status: "success", secrets: incSecrets ? "已加密" : "已排除" }, ...l]);
              toast({ tone: "ok", title: "备份已生成", body: "bk-20261104-1423 · 18.4 MB" });
            }}
          >
            立即备份
          </Button>
        </Card>
        <Card title="从备份恢复">
          <input
            ref={inputRef}
            type="file"
            accept=".spbak,.zip"
            className="hidden"
            onChange={(e) => {
              setFile(e.target.files?.[0]?.name ?? null);
              setPrechecked(false);
            }}
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button icon={<Upload size={14} />} onClick={() => inputRef.current?.click()}>
              选择备份文件
            </Button>
            <Button
              onClick={() => {
                setFile("bk-20261104-0300.spbak");
                setPrechecked(false);
              }}
            >
              使用最近一次备份
            </Button>
          </div>
          {file && (
            <div className="mt-3 flex items-center gap-2 text-[13px]">
              <FileCheck2 size={14} className="text-muted" /> <Mono>{file}</Mono>
              <Button size="sm" onClick={() => setPrechecked(true)}>
                预检
              </Button>
            </div>
          )}
          <p className="mt-3 text-[12px] text-muted">不支持导入其他系统的数据文件。</p>
        </Card>
      </Grid>

      {prechecked && !restored && (
        <Card title="恢复预检" className="mt-4" actions={<Button variant="danger" onClick={() => setConfirm(true)}>恢复</Button>}>
          <ul className="space-y-2.5">
            {precheckItems.map((p) => (
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

      {restored && (
        <Card title="恢复后业务核对" className="mt-4" desc="以下事项需逐一确认后再恢复正常运行">
          <Callout tone="ok" className="mb-4">恢复已完成。本地状态回到 2026-11-04 03:00，上游已发生的写入不会被回滚，也不会重新发送旧通知。</Callout>
          <div className="space-y-2">
            {postChecks.map((c) => (
              <Checkbox key={c} checked={done.includes(c)} onChange={(v) => setDone((d) => (v ? [...d, c] : d.filter((x) => x !== c)))} label={c} />
            ))}
          </div>
          <div className="mt-4 text-[12px] text-muted">
            已完成 {done.length} / {postChecks.length}
          </div>
        </Card>
      )}

      <Card title="备份记录" className="mt-4" pad={false}>
        <Table
          rows={list}
          rowKey={(b) => b.id}
          cols={[
            { key: "id", label: "备份", render: (b) => <Mono>{b.id}</Mono> },
            { key: "t", label: "时间", render: (b) => <span className="num">{b.at}</span> },
            { key: "by", label: "触发", render: (b) => b.by },
            { key: "s", label: "大小", render: (b) => b.size },
            { key: "v", label: "结构版本", render: (b) => <Mono>{b.schema}</Mono> },
            { key: "sec", label: "秘密", render: (b) => <Badge tone={b.secrets === "已加密" ? "ok" : "muted"}>{b.secrets}</Badge> },
            { key: "st", label: "结果", render: (b) => <Outcome k={b.status} /> },
            { key: "d", label: "", render: () => <Button size="sm" variant="ghost" icon={<Download size={12} />}>下载</Button> },
          ]}
        />
      </Card>

      <ConfirmModal
        open={confirm}
        onClose={() => setConfirm(false)}
        onConfirm={() => {
          setRestored(true);
          toast({ tone: "warn", title: "恢复完成", body: "所有会话已失效，请重新登录并完成业务核对。" });
        }}
        title="恢复本系统备份"
        phrase="恢复备份"
        confirmLabel="开始恢复"
        danger
      >
        <ul className="list-disc space-y-1 pl-5">
          <li>当前数据将被替换（已自动创建快照，失败时保持当前数据）</li>
          <li>恢复期间暂停采集与自动任务</li>
          <li>所有用户需要重新登录</li>
          <li>不会重放旧的远端操作</li>
        </ul>
      </ConfirmModal>
    </>
  );
}
