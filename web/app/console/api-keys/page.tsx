"use client";

import { Timestamp } from "@/components/timestamp";
import { useEffect, useRef, useState } from "react";
import { Plus, Copy, RefreshCw, Ban } from "lucide-react";
import { Badge, Button, Callout, Card, ConfirmModal, Field, Input, Modal, Mono, PageHeader, Select, Table, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { apiKey, type APIKey } from "@sub-pool/contracts/api-keys";
import { useAPIKeysData } from "@/lib/use-api-keys-data";

type Key = APIKey;
type Dialog = { kind: "new" } | { kind: "rotate" | "revoke"; key: Key } | null;

export default function ApiKeysPage() {
  const { toast, isAdmin, userName } = useApp();
  const data = useAPIKeysData();
  const [tab, setTab] = useState("keys");
  const [dialog, setDialog] = useState<Dialog>(null);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Key["mode"]>("read");
  const [secret, setSecret] = useState<string | null>(null);
  const sequence = useRef(0), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; sequence.current++; }; }, []);
  const newOpen = dialog?.kind === "new", rotate = dialog?.kind === "rotate" ? dialog.key : null, revoke = dialog?.kind === "revoke" ? dialog.key : null;
  const close = () => { sequence.current++; setDialog(null); setSecret(null); setName(""); setScope("read"); };
  const open = (next: Dialog) => {
    if (data.pending) return;
    sequence.current++; setSecret(null); setName(""); setScope("read"); data.clearNotice(); setDialog(next);
  };
  const reveal = (value: Awaited<ReturnType<typeof data.mutate>>, ticket: number) => {
    if (!value || !("secret" in value)) return false;
    if (!mounted.current || sequence.current !== ticket) { if (mounted.current) data.discardSecret(); return false; }
    setSecret(value.secret); setDialog({ kind: "new" }); return true;
  };
  const feedback = data.notice && <Callout tone={data.notice.needsReview ? "warn" : "info"}>{data.notice.text}</Callout>;

  return (
    <>
      <PageHeader
        title="API Key"
        desc="本系统的机器访问凭据。它与 Sub2API 的模型调用 Key、管理凭据相互独立。Key 的权限随所属用户的权限动态收缩。"
        actions={
          <Button variant="primary" icon={<Plus size={14} />} disabled={!data.writable} onClick={() => open({ kind: "new" })}>
            生成 Key
          </Button>
        }
      />
      <Tabs
        value={tab}
        onChange={(value) => { close(); setTab(value); void data.load(); }}
        tabs={[
          { id: "keys", label: "我的 Key" },
          { id: "docs", label: "接口说明" },
        ]}
      />

      {data.pending === "read" && <Callout>正在读取当前 Key 和接口说明…</Callout>}
      {data.error && <Callout tone="warn">{data.error} 点击当前标签重新读取；没有展示失败分页的部分记录。</Callout>}
      {feedback}
      {tab === "keys" && (
        <Card pad={false}>
          <Table
            rows={data.keys}
            rowKey={(k) => k.id}
            cols={[
              { key: "n", label: "名称", render: (k) => <span className="font-medium">{k.name}</span> },
              { key: "p", label: "前缀", render: (k) => <Mono>{k.prefix}…</Mono> },
              ...(isAdmin ? [{ key: "o", label: "所属", render: (k: Key) => <Mono>{userName}</Mono> }] : []),
              { key: "s", label: "权限", render: (k) => <Badge tone={k.mode === "management" ? "warn" : "muted"}>{k.mode === "management" ? "管理写：应用余额建议（未派发）" : isAdmin ? "只读查询" : "只读查询（本人范围）"}</Badge> },
              { key: "c", label: "创建时间", render: (k) => <Timestamp short className="num text-muted" value={k.created_at} empty="未知" /> },
              { key: "l", label: "最近使用", render: (k) => <Timestamp short className="num text-muted" value={k.last_used_at} empty="未知" /> },
              { key: "st", label: "状态", render: (k) => <Badge tone={k.revoked_at === null && (k.mode === "read" || isAdmin) ? "ok" : "muted"} dot>{k.revoked_at !== null ? "已撤销" : k.mode === "management" && !isAdmin ? "所属用户无管理权限" : "有效"}</Badge> },
              {
                key: "x",
                label: "",
                render: (k) =>
                  k.revoked_at === null && (
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" icon={<RefreshCw size={12} />} disabled={!data.writable || k.mode === "management" && !isAdmin} onClick={() => open({ kind: "rotate", key: k })}>
                        轮换
                      </Button>
                      <Button size="sm" variant="ghost" icon={<Ban size={12} />} disabled={!data.writable} onClick={() => open({ kind: "revoke", key: k })}>
                        撤销
                      </Button>
                    </div>
                  ),
              },
            ]}
          />
        </Card>
      )}

      {tab === "docs" && (
        <div className="space-y-4">
          <Card title="当前身份可用的接口" desc={`接口版本 ${data.catalog?.version ?? "未读取"} · 以 ${isAdmin ? "管理员" : "普通用户"} 身份展示；点击当前标签回读`} pad={false}>
            <Table
              rows={data.catalog?.operations ?? []}
              rowKey={(a) => a.name}
              cols={[
                { key: "g", label: "分组", render: (a) => a.feature || "API Key" },
                { key: "n", label: "能力", render: (a) => `${a.name} · ${a.path}` },
                { key: "m", label: "类型", render: (a) => <Mono>{a.method} · {a.contract}</Mono> },
                { key: "w", label: "权限", render: (a) => (a.management ? <Badge tone="warn">管理 Key · 仅记录未派发回执</Badge> : <Badge>只读 · 受数据范围约束</Badge>) },
              ]}
            />
          </Card>
          <Card title="错误说明">
            <ul className="space-y-2 text-[13px]">
              {(data.catalog?.errors ?? []).map(error => <li key={error.code}><Mono>{error.code} · {error.status}</Mono> <span className="text-muted">— {error.meaning}</span></li>)}
            </ul>
            {data.catalog && <Callout tone="info" className="mt-3">{!isAdmin && "你的 Key 只能读取被授权范围内的数据，写操作不对你开放。"}仅查询已保留事实，不代表完整账单；没有合并总览接口。管理 Key 的余额应用仍未派发。写入响应丢失后需回读核对，不能自动重放。</Callout>}
          </Card>
        </div>
      )}

      <Modal
        open={newOpen}
        onClose={close}
        title="生成 API Key"
        footer={
          secret ? (
            <Button
              variant="primary"
              onClick={close}
            >
              我已保存
            </Button>
          ) : (
            <>
              <Button onClick={close}>取消</Button>
              <Button
                variant="primary"
                disabled={!data.writable || !apiKey.shape.name.safeParse(name.trim()).success}
                onClick={async () => {
                  const ticket = sequence.current;
                  reveal(await data.mutate({ kind: "create", name: name.trim(), mode: scope }), ticket);
                }}
              >
                生成
              </Button>
            </>
          )
        }
      >
        {secret ? (
          <div className="space-y-3">
            <Callout tone="warn">完整 Key 只显示这一次，关闭后无法再次查看。</Callout>
            <div className="flex items-center gap-2 rounded-md border border-line bg-surface-2 p-2.5">
              <code className="flex-1 font-mono text-[12px] break-all">{secret}</code>
              <Button
                size="sm"
                icon={<Copy size={12} />}
                onClick={async () => {
                  const ticket = sequence.current;
                  try {
                    if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
                    await navigator.clipboard.writeText(secret);
                    if (mounted.current && sequence.current === ticket) toast({ tone: "ok", title: "已复制" });
                  } catch { if (mounted.current && sequence.current === ticket) toast({ tone: "bad", title: "复制未完成，请手动保存" }); }
                }}
              >
                复制
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <Field label="名称">
              <Input value={name} disabled={data.pending !== null} onChange={(e) => setName(e.target.value)} placeholder="例如：报表脚本" />
            </Field>
            <Field label="权限">
              <Select
                value={scope}
                disabled={data.pending !== null}
                onChange={(e) => { if (e.target.value === "read" || e.target.value === "management") setScope(e.target.value); }}
                options={[{ value: "read", label: "只读查询" }, ...(isAdmin ? [{ value: "management", label: "管理写：应用余额建议" }] : [])]}
              />
            </Field>
            {scope === "management" && <Callout tone="warn">管理 Key 与人工操作遵守同样限制和审计；当前余额应用只记录拒绝回执，不会派发。</Callout>}
            {feedback}
          </div>
        )}
      </Modal>

      <ConfirmModal
        open={!!rotate}
        onClose={close}
        disabled={!data.writable}
        onConfirm={async () => {
          if (!rotate) return false;
          const ticket = sequence.current;
          reveal(await data.mutate({ kind: "rotate", key: rotate }), ticket);
          return false;
        }}
        title={`轮换 ${rotate?.name ?? ""}`}
        confirmLabel="轮换"
      >
        旧 Key 将立即失效，并生成一个新 Key。
        {feedback}
      </ConfirmModal>
      <ConfirmModal
        open={!!revoke}
        onClose={close}
        disabled={!data.writable}
        onConfirm={async () => {
          if (!revoke) return false;
          return await data.mutate({ kind: "revoke", key: revoke }) !== null;
        }}
        title={`撤销 ${revoke?.name ?? ""}`}
        confirmLabel="撤销"
        danger
      >
        撤销后使用该 Key 的请求会立即被拒绝，此操作不可恢复。
        {feedback}
      </ConfirmModal>
    </>
  );
}
