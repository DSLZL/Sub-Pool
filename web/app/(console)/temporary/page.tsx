"use client";

import { useState } from "react";
import { Zap } from "lucide-react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Checkbox,
  Modal,
  PageHeader,
  Table,
  Tabs,
  cx,
} from "@/components/ui";
import { useTemporaryStatus } from "@/lib/use-temporary-status";

const checks = [
  { k: "数据新鲜度", note: "尚无本轮采集新鲜度核验依据。" },
  { k: "参与者绑定", note: "尚无本轮绑定与影响范围核验依据。" },
  { k: "官方周期", note: "尚未确认本轮相关账号的周期与截止时间。" },
  { k: "运行模式", note: "尚无本轮运行模式核验依据。" },
  { k: "钱包计费路径", note: "余额应用与安全收回尚未通过核验。" },
  { k: "钱包整体管理范围", note: "尚未确认完整钱包管理范围；不能按局部账号放行。" },
];

export default function TemporaryPage() {
  const data = useTemporaryStatus();
  const [selection, setSelection] = useState({ owner: "", tab: "round" });
  const [draft, setDraft] = useState<{ key: string; mode: "carry" | "no_carry"; informed: boolean; understand: boolean } | null>(null);
  const tab = selection.owner === data.owner ? selection.tab : "round", mode = draft?.mode ?? "carry";
  const message = !data.allowed ? "全局轮次仅管理员可查看。本人建议中的临时影响尚未接入，不能推断没有影响。"
    : data.status ? "临时参与不可用：余额应用与安全收回尚未通过核验。轮次与权益记录尚未接入，不能确认历史为空、普通建议已恢复或远端余额已收回。"
    : data.error ?? "正在读取临时参与可用性；尚无可确认的轮次记录。";

  return (
    <>
      <PageHeader
        title="临时放宽"
        tag="S2A-TEMP-001 ~ 010"
        desc="在一个明确轮次内临时放宽余额建议。它只影响本地建议和按授权应用的钱包目标，不会增加上游订阅容量。"
        actions={
          data.allowed && (
            <Button variant="primary" icon={<Zap size={14} />} onClick={() => {
              if (data.error) { setDraft(null); data.reload(); }
              else setDraft({ key: data.key, mode: "carry", informed: false, understand: false });
            }}>
              {data.error ? "重新读取" : "启用前核验"}
            </Button>
          )
        }
      />

      {data.allowed && <Tabs
        value={tab}
        onChange={(id) => { setDraft(null); setSelection({ owner: data.owner, tab: id }); if (id === tab) data.reload(); }}
        tabs={[
          { id: "round", label: "当前轮次" },
          { id: "settle", label: "后续权益处理" },
        ]}
      />}

      {(!data.allowed || tab === "round") && (
          <Card>
            <p className="text-[13px] text-muted" role="status">{message}</p>
          </Card>
        )}

      {data.allowed && tab === "settle" && (
        <Card pad={false} title="待结算、异常与完成记录" desc={message}>
          <Table
            rows={[]}
            rowKey={() => ""}
            empty="权益记录不可用；尚未读取待结算、异常或已完成记录。"
            cols={[
              { key: "r", label: "轮次", render: () => null },
              { key: "a", label: "账号 / 原周期", render: () => null },
              { key: "m", label: "成员", render: () => null },
              { key: "v", label: "权益调整", align: "right", render: () => null },
              { key: "s", label: "状态", render: () => null },
              { key: "why", label: "依据 / 原因", render: () => null },
              { key: "x", label: "", render: () => null },
            ]}
          />
        </Card>
      )}

      {/* shortcut: only availability is qualified; enable mutations after round and safe withdrawal acceptance. */}
      <Modal
        open={data.allowed && draft?.key === data.key}
        onClose={() => setDraft(null)}
        title="开启临时放宽轮次"
        wide
        footer={
          <>
            <Button onClick={() => setDraft(null)}>取消</Button>
            <Button
              variant="primary"
              disabled
            >
              开启轮次
            </Button>
          </>
        }
      >
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <div className="space-y-3">
            <div className="text-[13px] font-semibold">启用前核验</div>
            <ul className="space-y-2">
              {checks.map((c) => (
                <li key={c.k} className="flex items-start gap-2 text-[12.5px]">
                  <Badge tone="warn">未核验</Badge>
                  <div>
                    <div className="font-medium">{c.k}</div>
                    <div className="text-muted">{c.note}</div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
          <div className="space-y-4">
            <div className="text-[13px] font-semibold">选择模式</div>
            {(
              [
                ["carry", "结转", "本轮借用的权益在后续周期补偿或扣减。"],
                ["no_carry", "不结转", "不新增后续补偿；需确认已告知参与者。"],
              ] as const
            ).map(([id, label, desc]) => (
              <button
                key={id}
                disabled={!data.status}
                aria-pressed={mode === id}
                onClick={() => setDraft(previous => previous ? { ...previous, mode: id, informed: false } : null)}
                className={cx("w-full rounded-md border p-3 text-left", mode === id ? "border-brand bg-brand-soft" : "border-line hover:bg-surface-2")}
              >
                <div className="text-[13px] font-medium">{label}</div>
                <div className="text-[12px] text-muted">{desc}</div>
              </button>
            ))}
            <Callout tone="info">{message} 影响范围尚未核验；此处选择与勾选只作本次查看，不保存，也不会发信。新轮提醒不可用，不能继承旧轮设置。</Callout>
            {mode === "no_carry" && <Checkbox checked={draft?.informed ?? false} onChange={(informed) => setDraft(previous => previous ? { ...previous, informed } : null)} label="我已告知所有受影响参与者（此勾选仅记录业务确认，系统不会逐人发邮件）" />}
            <Checkbox checked={draft?.understand ?? false} onChange={(understand) => setDraft(previous => previous ? { ...previous, understand } : null)} label="我了解：结束建议不等于收回上游余额，需单独核对。" />
          </div>
        </div>
      </Modal>

    </>
  );
}
