"use client";

import { useState } from "react";
import { Plus, Copy, RefreshCw, Ban } from "lucide-react";
import { Badge, Button, Callout, Card, ConfirmModal, Field, Input, Modal, Mono, PageHeader, Select, Table, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { apiCatalog, apiKeys as initial } from "@/lib/mock";

type Key = (typeof initial)[number];

export default function ApiKeysPage() {
  const { toast, isAdmin, userName } = useApp();
  const [tab, setTab] = useState("keys");
  const [keys, setKeys] = useState<Key[]>(initial);
  const [newOpen, setNewOpen] = useState(false);
  const [name, setName] = useState("");
  const [scope, setScope] = useState("read");
  const [secret, setSecret] = useState<string | null>(null);
  const [revoke, setRevoke] = useState<Key | null>(null);
  const [rotate, setRotate] = useState<Key | null>(null);

  const mine = keys.filter((k) => isAdmin || k.owner === userName);
  const gen = () => `spk_live_${Math.random().toString(36).slice(2, 6)}${Math.random().toString(36).slice(2, 30)}`;

  return (
    <>
      <PageHeader
        title="API Key"
        tag="S2A-API-001 ~ 004"
        desc="本系统的机器访问凭据。它与 Sub2API 的模型调用 Key、管理凭据相互独立。Key 的权限随所属用户的权限动态收缩。"
        actions={
          <Button variant="primary" icon={<Plus size={14} />} onClick={() => setNewOpen(true)}>
            生成 Key
          </Button>
        }
      />
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "keys", label: isAdmin ? "全部 Key" : "我的 Key" },
          { id: "docs", label: "接口说明" },
        ]}
      />

      {tab === "keys" && (
        <Card pad={false}>
          <Table
            rows={mine}
            rowKey={(k) => k.id}
            cols={[
              { key: "n", label: "名称", render: (k) => <span className="font-medium">{k.name}</span> },
              { key: "p", label: "前缀", render: (k) => <Mono>{k.prefix}…</Mono> },
              ...(isAdmin ? [{ key: "o", label: "所属", render: (k: Key) => <Mono>{k.owner}</Mono> }] : []),
              { key: "s", label: "权限", render: (k) => <Badge tone={k.scope.startsWith("管理写") ? "warn" : "muted"}>{k.scope}</Badge> },
              { key: "c", label: "创建", render: (k) => <span className="num text-muted">{k.created}</span> },
              { key: "l", label: "最近使用", render: (k) => <span className="num text-muted">{k.lastUsed}</span> },
              { key: "st", label: "状态", render: (k) => <Badge tone={k.status === "active" ? "ok" : "muted"} dot>{k.status === "active" ? "有效" : "已撤销"}</Badge> },
              {
                key: "x",
                label: "",
                render: (k) =>
                  k.status === "active" && (
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" icon={<RefreshCw size={12} />} onClick={() => setRotate(k)}>
                        轮换
                      </Button>
                      <Button size="sm" variant="ghost" icon={<Ban size={12} />} onClick={() => setRevoke(k)}>
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
          <Card title="当前身份可用的接口" desc={`接口版本 v1 · 以 ${isAdmin ? "管理员" : "普通用户"} 身份展示`} pad={false}>
            <Table
              rows={apiCatalog.filter((a) => isAdmin || !a.write)}
              rowKey={(a) => a.name}
              cols={[
                { key: "g", label: "分组", render: (a) => a.group },
                { key: "n", label: "能力", render: (a) => a.name },
                { key: "m", label: "类型", render: (a) => <Mono>{a.method}</Mono> },
                { key: "w", label: "权限", render: (a) => (a.write ? <Badge tone="warn">管理写</Badge> : <Badge>只读 · 受数据范围约束</Badge>) },
              ]}
            />
          </Card>
          <Card title="错误说明">
            <ul className="space-y-2 text-[13px]">
              <li><Mono>UNAUTHORIZED</Mono> <span className="text-muted">— Key 无效、已撤销或所属用户已停用</span></li>
              <li><Mono>FORBIDDEN</Mono> <span className="text-muted">— 功能权限或数据范围不足；只读 Key 调用写操作</span></li>
              <li><Mono>PRECONDITION_FAILED</Mono> <span className="text-muted">— 建议已失效、依据不足或能力门禁未通过</span></li>
              <li><Mono>OUTCOME_UNKNOWN</Mono> <span className="text-muted">— 上游写入结果未知，需人工核对</span></li>
            </ul>
            {!isAdmin && <Callout tone="info" className="mt-3">你的 Key 只能读取你被授权范围内的数据，写操作接口不对你开放。</Callout>}
          </Card>
        </div>
      )}

      <Modal
        open={newOpen}
        onClose={() => {
          setNewOpen(false);
          setSecret(null);
          setName("");
        }}
        title="生成 API Key"
        footer={
          secret ? (
            <Button
              variant="primary"
              onClick={() => {
                setNewOpen(false);
                setSecret(null);
                setName("");
              }}
            >
              我已保存
            </Button>
          ) : (
            <>
              <Button onClick={() => setNewOpen(false)}>取消</Button>
              <Button
                variant="primary"
                disabled={!name.trim()}
                onClick={() => {
                  const s = gen();
                  setSecret(s);
                  setKeys((ks) => [
                    {
                      id: `ak_${ks.length + 1}`,
                      name,
                      owner: userName,
                      scope: scope === "write" ? "管理写：应用余额建议" : isAdmin ? "只读查询" : "只读查询（本人范围）",
                      prefix: s.slice(0, 13),
                      created: "2026-11-04",
                      lastUsed: "—",
                      status: "active",
                    },
                    ...ks,
                  ]);
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
                onClick={() => {
                  navigator.clipboard?.writeText(secret).catch(() => {});
                  toast({ tone: "ok", title: "已复制" });
                }}
              >
                复制
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <Field label="名称">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：报表脚本" />
            </Field>
            <Field label="权限">
              <Select
                value={scope}
                onChange={(e) => setScope(e.target.value)}
                options={[{ value: "read", label: "只读查询" }, ...(isAdmin ? [{ value: "write", label: "管理写：应用余额建议" }] : [])]}
              />
            </Field>
            {scope === "write" && <Callout tone="warn">管理写 Key 可触发与人工操作相同的余额应用，共享同样的业务限制与审计。</Callout>}
          </div>
        )}
      </Modal>

      <ConfirmModal
        open={!!rotate}
        onClose={() => setRotate(null)}
        onConfirm={() => {
          const s = gen();
          setKeys((ks) => ks.map((k) => (k.id === rotate?.id ? { ...k, prefix: s.slice(0, 13) } : k)));
          setSecret(s);
          setNewOpen(true);
        }}
        title={`轮换 ${rotate?.name ?? ""}`}
        confirmLabel="轮换"
      >
        旧 Key 将立即失效，并生成一个新 Key。
      </ConfirmModal>
      <ConfirmModal
        open={!!revoke}
        onClose={() => setRevoke(null)}
        onConfirm={() => {
          setKeys((ks) => ks.map((k) => (k.id === revoke?.id ? { ...k, status: "revoked" } : k)));
          toast({ tone: "ok", title: `${revoke?.name} 已撤销` });
        }}
        title={`撤销 ${revoke?.name ?? ""}`}
        confirmLabel="撤销"
        danger
      >
        撤销后使用该 Key 的请求会立即被拒绝，此操作不可恢复。
      </ConfirmModal>
    </>
  );
}
