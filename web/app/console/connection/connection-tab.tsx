import { glossary, uiCopy } from "@/lib/glossary";
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
          <Field label="服务地址" hint={`只支持单一 ${glossary.sub2api} 实例。`}>
            <Input value={url} disabled={locked} maxLength={2048} onChange={e => setAddress(e.target.value)} />
          </Field>
          {urlChanged && (
            <Callout tone="warn" title="更换地址">
              请核对未决操作。
            </Callout>
          )}
          <Field label="管理凭据" hint="保存后隐藏。">
            <div className="mb-2 flex items-center gap-2">
              {credSet ? <Badge tone="ok">已配置</Badge> : <Badge tone="muted">未配置</Badge>}
              {credSet && (
                <button className="text-[12px] text-bad hover:underline" aria-label="清除管理凭据" disabled={locked} onClick={openCredentialClear}>
                  清除
                </button>
              )}
            </div>
            <Input type="password" placeholder={credSet ? "填写新凭据" : "输入管理凭据"} value={newSecret} disabled={locked} maxLength={8192} onChange={e => setCredential(e.target.value)} autoComplete="off" />
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
      <Card title="测试结果">
        {!shownTest && !testing && <p className="text-[13px] text-muted">{pending === "read" ? uiCopy.loading : "暂无测试结果。"}</p>}
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
