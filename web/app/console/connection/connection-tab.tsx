import { Plug } from "lucide-react";
import { Badge, Button, Callout, Card, Field, Grid, Input, Outcome } from "@/components/ui";
import { capabilityNames, checkText } from "@/lib/connection-data";
import type { ConnectionConsole } from "./use-connection-console";

export function ConnectionTab({ view }: { view: ConnectionConsole["connection"] }) {
  const {
    saved, url, newSecret, urlChanged, credSet, locked, shownTest, testing, pending, time,
    setAddress, setCredential, openCredentialClear, runTest, requestSave,
  } = view;
  return (
    <Grid cols={2}>
      <Card title="连接配置" desc={`最近保存 ${time(saved?.saved_at)}`}>
        <div className="space-y-4">
          <Field label="服务地址" hint="只支持单一 Sub2API 实例，不提供平台切换。">
            <Input value={url} disabled={locked} maxLength={2048} onChange={e => setAddress(e.target.value)} />
          </Field>
          {urlChanged && (
            <Callout tone="warn" title="目标地址将变更">
              更换后，现有账号映射、未完成的余额写入和本系统托管的计价修改都需要重新核对。同名或同 ID 不代表同一资源，旧写入不会被迁移或重放。
            </Callout>
          )}
          <Field label="管理凭据" hint="保存后不再显示原文，只显示是否已配置。">
            <div className="mb-2 flex items-center gap-2">
              {credSet ? <Badge tone="ok">已配置（原文不可读取）</Badge> : <Badge tone="muted">未配置</Badge>}
              {credSet && (
                <button className="text-[12px] text-bad hover:underline" aria-label="清除管理凭据" disabled={locked} onClick={openCredentialClear}>
                  清除
                </button>
              )}
            </div>
            <Input type="password" placeholder={credSet ? "输入新凭据以替换" : "输入管理凭据"} value={newSecret} disabled={locked} maxLength={8192} onChange={e => setCredential(e.target.value)} autoComplete="off" />
          </Field>
          <div className="flex flex-wrap gap-2">
            <Button icon={<Plug size={14} />} onClick={() => runTest()} disabled={locked || !url}>
              {testing ? "测试中…" : "测试连接"}
            </Button>
            <Button
              variant="primary"
              disabled={locked || !url}
              onClick={requestSave}
            >
              保存
            </Button>
          </div>
        </div>
      </Card>
      <Card title="测试结果" desc="可在保存前测试未保存的参数。服务可达不代表管理能力可用。">
        {!shownTest && !testing && <p className="text-[13px] text-muted">{pending === "read" ? "正在读取已保存状态…" : "暂无已确认的测试结果。测试不保存地址或凭据。"}</p>}
        {testing && <p className="text-[13px] text-muted">正在逐步检查…</p>}
        {shownTest && !testing && (
          <ol className="space-y-2.5">
            {shownTest.checks.map((t) => (
              <li key={t.id} className="flex items-start gap-3">
                <Outcome k={t.state === "available" ? "success" : t.state === "failed" || t.state === "denied" ? "failed" : t.state === "unsupported" ? "skipped" : "unknown"} />
                <div>
                  <div className="text-[13px] font-medium">{capabilityNames[t.id]}</div>
                  <div className="text-[12px] text-muted">{checkText(t, shownTest.version)} · {shownTest.saved ? "已保存目标" : "未保存参数"} · {time(shownTest.checked_at)}</div>
                </div>
              </li>
            ))}
          </ol>
        )}
      </Card>
    </Grid>
  );
}
