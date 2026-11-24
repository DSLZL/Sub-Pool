import { Badge, Button, Card, Field, Grid, Input, KV, Select, Switch } from "@/components/ui";
import type { ConnectionConsole } from "./use-connection-console";

export function CollectionTab({ view }: { view: ConnectionConsole["collection"] }) {
  const {
    job, locked, setJob, saveJob, status, time, lastRun,
  } = view;
  return (
    <Grid cols={2}>
      <Card title="采集安排">
        <div className="space-y-4">
          <Switch checked={job.enabled} disabled={locked} onChange={(v) => setJob({ ...job, enabled: v })} label="启用定时采集" desc="关闭后不会自动收回远端余额；临时模式的恢复义务仍需单独处理。" />
          <Field label="常规频率（分钟）">
            <Select value={job.interval} disabled={locked} onChange={(e) => setJob({ ...job, interval: e.target.value })} options={[...new Set([job.interval, "5", "10", "15", "30", "60"])]} />
          </Field>
          <Switch checked={job.usageEnabled} disabled={locked} onChange={(v) => setJob({ ...job, usageEnabled: v })} label="定时采集用量" desc="启用定时采集后，后台按 UTC 日期接续已纳管账号的用量。" />
          <Field label="用量采集频率（分钟）">
            <Input type="number" min={1} max={1440} disabled={locked} value={job.usageInterval} onChange={(e) => setJob({ ...job, usageInterval: e.target.value })} />
          </Field>
          <Switch checked={job.refreshOnChange} disabled={locked} onChange={(v) => setJob({ ...job, refreshOnChange: v })} label="业务变化后刷新" desc="账号、连接或采集设置变化后优先重新采集，仍遵守最小间隔与限流等待。" />
          <Switch checked={job.boost} disabled={locked} onChange={(v) => setJob({ ...job, boost: v })} label="临近重置时加密观察" desc="在窗口重置前后提高采集频率；仍遵守最小间隔和限流等待。" />
          <Field label="临近重置观察提前量（分钟）">
            <Input type="number" min={1} max={1440} disabled={locked} value={job.nearLead} onChange={(e) => setJob({ ...job, nearLead: e.target.value })} />
          </Field>
          <Field label="临近重置观察频率（分钟）">
            <Input type="number" min={1} max={1440} disabled={locked} value={job.nearInterval} onChange={(e) => setJob({ ...job, nearInterval: e.target.value })} />
          </Field>
          <Switch checked={job.staleWarnings} disabled={locked} onChange={(v) => setJob({ ...job, staleWarnings: v })} label="启用数据过旧提醒" desc="关闭只停止过旧提醒，不会让数据变新，也不改变采集安排。" />
          <Field label="数据过旧提醒阈值（分钟）">
            <Input type="number" min={1} max={10080} disabled={locked} value={job.stale} onChange={(e) => setJob({ ...job, stale: e.target.value })} />
          </Field>
          <Button variant="primary" disabled={locked} onClick={saveJob}>
            保存
          </Button>
        </div>
      </Card>
      <Card title="当前生效">
        <KV
          cols={1}
          items={[
            ["状态", !status ? "尚未读取" : status.enabled ? <Badge tone="ok" dot>已启用 · 后台{status.worker_recent ? "最近在线" : "待核对"}</Badge> : <Badge tone="muted" dot>已暂停</Badge>],
            ["下次执行", <span key="n" className="num">{status ? status.next_scheduled_at ? `${time(status.next_scheduled_at)}（计划）` : status.enabled ? "暂无待安排账号" : "已暂停" : "尚未读取"}</span>],
            ["最近批次成功数", <span key="s" className="num">{lastRun?.counts.succeeded ?? "暂无任务"}</span>],
            ["最近记录（读取时）", lastRun ? `最近成功 ${time(status?.last_success_at)} · 最近批次 ${time(lastRun.created_at)} · ${lastRun.state} · 成功 ${lastRun.counts.succeeded} / 失败 ${lastRun.counts.failed} / 跳过 ${lastRun.counts.skipped} / 冲突 ${lastRun.counts.conflict} / 中断 ${lastRun.counts.interrupted} / 排队 ${lastRun.counts.queued} / 执行 ${lastRun.counts.running}` : "暂无任务"],
          ]}
        />
      </Card>
    </Grid>
  );
}

export function SettingsTab({ view }: { view: ConnectionConsole["settings"] }) {
  const {
    tz, locked, setTz, settings, saveTimezone,
  } = view;
  return (
    <Card title="统计时区" desc="改变显示与统计时区不会改变原始时刻或上游重置时间。" className="max-w-xl">
      <div className="space-y-4">
        <Field label="时区">
          <Select value={tz} disabled={locked} onChange={(e) => setTz(e.target.value)} options={[...new Set([...(settings ? [settings.timezone] : []), "Asia/Shanghai", "Asia/Tokyo", "UTC", "Europe/Berlin", "America/Los_Angeles"])]} />
        </Field>
        <Button variant="primary" disabled={locked} onClick={saveTimezone}>
          保存
        </Button>
      </div>
    </Card>
  );
}
