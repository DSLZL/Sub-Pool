"use client";

import { useEffect, useId, useRef, useState } from "react";
import { X, Info, AlertTriangle, CheckCircle2, CircleHelp, Lock, Clock3, XCircle } from "lucide-react";
import { useApp } from "@/lib/store";

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(" ");
}

/* ---------- Layout primitives ---------- */

export function PageHeader({
  title,
  desc,
  actions,
  tag,
}: {
  title: string;
  desc?: React.ReactNode;
  actions?: React.ReactNode;
  tag?: string;
}) {
  return (
    <div className="mb-5 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        {tag && <div className="mb-1 font-mono text-[11px] tracking-wide text-faint">{tag}</div>}
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {desc && <p className="mt-1 max-w-3xl text-[13px] leading-relaxed text-muted">{desc}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function Card({
  title,
  desc,
  actions,
  children,
  className,
  pad = true,
}: {
  title?: React.ReactNode;
  desc?: React.ReactNode;
  actions?: React.ReactNode;
  children?: React.ReactNode;
  className?: string;
  pad?: boolean;
}) {
  return (
    <section className={cx("rounded-lg border border-line bg-surface shadow-[var(--shadow)]", className)}>
      {(title || actions) && (
        <header className="flex flex-wrap items-start justify-between gap-2 border-b border-line px-4 py-3">
          <div className="min-w-0">
            {title && <h2 className="text-[14px] font-semibold">{title}</h2>}
            {desc && <p className="mt-0.5 text-[12px] text-muted">{desc}</p>}
          </div>
          {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={pad ? "p-4" : ""}>{children}</div>
    </section>
  );
}

export function Grid({ children, cols = 3, className }: { children: React.ReactNode; cols?: 2 | 3 | 4; className?: string }) {
  const c = { 2: "lg:grid-cols-2", 3: "lg:grid-cols-3", 4: "grid-cols-2 xl:grid-cols-4" }[cols];
  return <div className={cx("grid gap-4", cols === 4 ? "" : "grid-cols-1", c, className)}>{children}</div>;
}

/* ---------- Buttons & inputs ---------- */

type BtnProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "sm" | "md";
  icon?: React.ReactNode;
};

export function Button({ variant = "secondary", size = "md", icon, className, children, ...rest }: BtnProps) {
  const v = {
    primary: "bg-brand text-brand-ink hover:opacity-90 border-transparent",
    secondary: "bg-surface text-ink border-line hover:bg-surface-2",
    ghost: "bg-transparent text-muted border-transparent hover:bg-surface-2 hover:text-ink",
    danger: "bg-bad text-white border-transparent hover:opacity-90",
  }[variant];
  const s = size === "sm" ? "h-7 px-2.5 text-[12px] gap-1" : "h-8.5 px-3 text-[13px] gap-1.5";
  return (
    <button
      {...rest}
      className={cx(
        "inline-flex shrink-0 items-center justify-center rounded-md border font-medium whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-45",
        v,
        s,
        className,
      )}
    >
      {icon}
      {children}
    </button>
  );
}

/** A write action: hidden behind role. Non-admins see a locked, disabled button with a reason (UI only; server decides). */
export function AdminButton(props: BtnProps & { reason?: string }) {
  const { isAdmin } = useApp();
  const { reason, ...rest } = props;
  if (isAdmin) return <Button {...rest} />;
  return (
    <Button {...rest} disabled title={reason ?? "当前身份为只读访问，此操作需要管理写权限"} icon={<Lock size={13} />} onClick={undefined} />
  );
}

export function Field({
  label,
  hint,
  error,
  children,
  className,
}: {
  label: string;
  hint?: React.ReactNode;
  error?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <label className={cx("block", className)}>
      <span className="mb-1 block text-[12px] font-medium text-muted">{label}</span>
      {children}
      {hint && !error && <span className="mt-1 block text-[11.5px] text-faint">{hint}</span>}
      {error && <span className="mt-1 block text-[11.5px] text-bad">{error}</span>}
    </label>
  );
}

const inputCls =
  "w-full rounded-md border border-line bg-surface px-2.5 py-1.5 text-[13px] text-ink placeholder:text-faint focus:border-brand focus:outline-none disabled:bg-surface-2 disabled:text-muted";

export function Input(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx(inputCls, props.className)} />;
}

export function Textarea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea rows={3} {...props} className={cx(inputCls, props.className)} />;
}

export function Select({
  options,
  className,
  ...rest
}: React.SelectHTMLAttributes<HTMLSelectElement> & { options: (string | { value: string; label: string })[] }) {
  return (
    <select {...rest} className={cx(inputCls, "pr-7", className)}>
      {options.map((o) =>
        typeof o === "string" ? (
          <option key={o} value={o}>
            {o}
          </option>
        ) : (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ),
      )}
    </select>
  );
}

export function Switch({
  checked,
  onChange,
  label,
  disabled,
  desc,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
  disabled?: boolean;
  desc?: React.ReactNode;
}) {
  const id = useId();
  return (
    <div className="flex items-start justify-between gap-4">
      {label && (
        <label htmlFor={id} className="min-w-0">
          <span className="block text-[13px] font-medium">{label}</span>
          {desc && <span className="mt-0.5 block text-[12px] text-muted">{desc}</span>}
        </label>
      )}
      <button
        id={id}
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cx(
          "relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-45",
          checked ? "bg-brand" : "bg-line",
        )}
      >
        <span
          className={cx(
            "absolute top-0.5 left-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform",
            checked && "translate-x-4",
          )}
        />
      </button>
    </div>
  );
}

export function Checkbox({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: React.ReactNode; disabled?: boolean }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 text-[13px]">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-0.5 accent-[var(--brand)]" />
      <span>{label}</span>
    </label>
  );
}

/* ---------- Status vocabulary ---------- */

export type Tone = "ok" | "bad" | "warn" | "info" | "unk" | "muted" | "brand";

const toneCls: Record<Tone, string> = {
  ok: "bg-ok-soft text-ok",
  bad: "bg-bad-soft text-bad",
  warn: "bg-warn-soft text-warn",
  info: "bg-info-soft text-info",
  unk: "bg-unk-soft text-unk",
  muted: "bg-surface-2 text-muted",
  brand: "bg-brand-soft text-brand",
};

export function Badge({ tone = "muted", children, dot, className }: { tone?: Tone; children: React.ReactNode; dot?: boolean; className?: string }) {
  return (
    <span className={cx("inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11.5px] font-medium whitespace-nowrap", toneCls[tone], className)}>
      {dot && <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}

/** Shared outcome vocabulary for remote writes and jobs. */
export const OUTCOME: Record<string, { tone: Tone; label: string }> = {
  confirmed: { tone: "ok", label: "已确认" },
  success: { tone: "ok", label: "成功" },
  failed: { tone: "bad", label: "失败" },
  unknown: { tone: "unk", label: "结果未知" },
  reconcile: { tone: "warn", label: "待人工核对" },
  partial: { tone: "warn", label: "部分完成" },
  pending: { tone: "info", label: "处理中" },
  queued: { tone: "info", label: "已排队" },
  skipped: { tone: "muted", label: "已跳过" },
  conflict: { tone: "warn", label: "冲突" },
  blocked: { tone: "bad", label: "阻塞" },
  rejected: { tone: "bad", label: "已拒绝" },
  cancelled: { tone: "muted", label: "已取消" },
  expired: { tone: "muted", label: "已失效" },
};

export function Outcome({ k }: { k: keyof typeof OUTCOME | string }) {
  const o = OUTCOME[k] ?? { tone: "muted" as Tone, label: k };
  return (
    <Badge tone={o.tone} dot>
      {o.label}
    </Badge>
  );
}

/** Render a value that may be unknown. Unknown is never displayed as zero. */
export function Val({ v, unit, digits = 2, prefix }: { v: number | null | undefined; unit?: string; digits?: number; prefix?: string }) {
  if (v === null || v === undefined)
    return (
      <span className="inline-flex items-center gap-1 text-unk" title="数据未知：来源未返回或无法确认，不等于 0">
        <CircleHelp size={12} />
        未知
      </span>
    );
  return (
    <span className="num">
      {prefix}
      {v.toLocaleString("zh-CN", { minimumFractionDigits: digits, maximumFractionDigits: digits })}
      {unit && <span className="ml-0.5 text-muted">{unit}</span>}
    </span>
  );
}

export type Freshness = "fresh" | "stale" | "missing" | "failed" | "unknown";
const freshMap: Record<Freshness, { tone: Tone; label: string }> = {
  fresh: { tone: "ok", label: "新鲜" },
  stale: { tone: "warn", label: "已过期" },
  missing: { tone: "muted", label: "缺失" },
  failed: { tone: "bad", label: "获取失败" },
  unknown: { tone: "unk", label: "源时间未知" },
};

export function FreshTag({ s, at }: { s: Freshness; at?: string | null }) {
  const m = freshMap[s];
  return (
    <span className="inline-flex items-center gap-1.5">
      <Badge tone={m.tone}>
        <Clock3 size={11} />
        {m.label}
      </Badge>
      {at !== undefined && <span className="num text-[12px] text-muted">{at ?? "—"}</span>}
    </span>
  );
}

export type Cap = "available" | "noperm" | "unsupported" | "unverified" | "gated";
export const CAP: Record<Cap, { tone: Tone; label: string }> = {
  available: { tone: "ok", label: "可用" },
  noperm: { tone: "bad", label: "缺权限" },
  unsupported: { tone: "muted", label: "不支持" },
  unverified: { tone: "unk", label: "未验证" },
  gated: { tone: "warn", label: "门禁未通过" },
};
export function CapBadge({ c }: { c: Cap }) {
  return (
    <Badge tone={CAP[c].tone} dot>
      {CAP[c].label}
    </Badge>
  );
}

/* ---------- Callouts ---------- */

export function Callout({
  tone = "info",
  title,
  children,
  className,
}: {
  tone?: "info" | "warn" | "bad" | "ok" | "unk";
  title?: React.ReactNode;
  children?: React.ReactNode;
  className?: string;
}) {
  const Icon = { info: Info, warn: AlertTriangle, bad: XCircle, ok: CheckCircle2, unk: CircleHelp }[tone];
  const cls = {
    info: "border-info/30 bg-info-soft",
    warn: "border-warn/30 bg-warn-soft",
    bad: "border-bad/30 bg-bad-soft",
    ok: "border-ok/30 bg-ok-soft",
    unk: "border-unk/30 bg-unk-soft",
  }[tone];
  const ic = { info: "text-info", warn: "text-warn", bad: "text-bad", ok: "text-ok", unk: "text-unk" }[tone];
  return (
    <div className={cx("flex gap-2.5 rounded-md border px-3 py-2.5 text-[12.5px] leading-relaxed", cls, className)}>
      <Icon size={16} className={cx("mt-0.5 shrink-0", ic)} />
      <div className="min-w-0 text-ink">
        {title && <div className="font-semibold">{title}</div>}
        {children && <div className={title ? "mt-0.5 text-ink/80" : ""}>{children}</div>}
      </div>
    </div>
  );
}

/** Capability gate notice: shown when a write path is not yet opened. */
export function GateNotice({ gate, children }: { gate: string; children?: React.ReactNode }) {
  return (
    <Callout tone="warn" title={`能力门禁：${gate}`}>
      {children ?? "前提条件未全部通过核验，相关写入保持关闭。界面不会以本地记录或 2xx 响应代替上游确认。"}
    </Callout>
  );
}

/* ---------- Data display ---------- */

const toneText: Record<Tone, string> = {
  ok: "text-ok",
  bad: "text-bad",
  warn: "text-warn",
  info: "text-info",
  unk: "text-unk",
  muted: "text-muted",
  brand: "text-brand",
};

export function Stat({
  label,
  value,
  sub,
  tone,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  tone?: Tone;
}) {
  return (
    <div className="rounded-lg border border-line bg-surface p-4 shadow-[var(--shadow)]">
      <div className="text-[12px] text-muted">{label}</div>
      <div className={cx("num mt-1.5 text-[22px] font-semibold tracking-tight", tone && toneText[tone])}>{value}</div>
      {sub && <div className="mt-1 text-[12px] text-muted">{sub}</div>}
    </div>
  );
}

export function KV({ items, cols = 2 }: { items: [string, React.ReactNode][]; cols?: 1 | 2 | 3 }) {
  const c = { 1: "", 2: "sm:grid-cols-2", 3: "sm:grid-cols-3" }[cols];
  return (
    <dl className={cx("grid grid-cols-1 gap-x-6 gap-y-3", c)}>
      {items.map(([k, v]) => (
        <div key={k} className="min-w-0">
          <dt className="text-[12px] text-muted">{k}</dt>
          <dd className="mt-0.5 text-[13px] break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export type Col<T> = { key: string; label: React.ReactNode; render: (r: T) => React.ReactNode; className?: string; align?: "right" };

export function Table<T>({
  cols,
  rows,
  rowKey,
  empty,
  onRow,
}: {
  cols: Col<T>[];
  rows: T[];
  rowKey: (r: T) => string;
  empty?: React.ReactNode;
  onRow?: (r: T) => void;
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-[13px]">
        <thead>
          <tr className="border-b border-line bg-surface-2/60 text-left">
            {cols.map((c) => (
              <th
                key={c.key}
                className={cx("px-3 py-2 text-[12px] font-medium whitespace-nowrap text-muted", c.align === "right" && "text-right", c.className)}
              >
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr>
              <td colSpan={cols.length} className="px-3 py-10 text-center text-muted">
                {empty ?? "暂无记录"}
              </td>
            </tr>
          )}
          {rows.map((r) => (
            <tr
              key={rowKey(r)}
              onClick={onRow ? () => onRow(r) : undefined}
              className={cx("border-b border-line last:border-0", onRow && "cursor-pointer hover:bg-surface-2/60")}
            >
              {cols.map((c) => (
                <td key={c.key} className={cx("px-3 py-2.5 align-middle", c.align === "right" && "text-right", c.className)}>
                  {c.render(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Tabs({ tabs, value, onChange }: { tabs: { id: string; label: React.ReactNode }[]; value: string; onChange: (id: string) => void }) {
  return (
    <div role="tablist" className="mb-4 flex gap-1 overflow-x-auto border-b border-line">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          aria-selected={value === t.id}
          onClick={() => onChange(t.id)}
          className={cx(
            "-mb-px border-b-2 px-3 py-2 text-[13px] font-medium whitespace-nowrap transition-colors",
            value === t.id ? "border-brand text-ink" : "border-transparent text-muted hover:text-ink",
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

export function Segmented({ options, value, onChange }: { options: { id: string; label: string }[]; value: string; onChange: (v: string) => void }) {
  return (
    <div className="inline-flex rounded-md border border-line bg-surface-2 p-0.5">
      {options.map((o) => (
        <button
          key={o.id}
          onClick={() => onChange(o.id)}
          className={cx(
            "rounded px-2.5 py-1 text-[12px] font-medium transition-colors",
            value === o.id ? "bg-surface text-ink shadow-sm" : "text-muted hover:text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Progress({ value, tone = "brand", label }: { value: number | null; tone?: "brand" | "warn" | "bad" | "ok"; label?: string }) {
  if (value === null)
    return (
      <div className="h-1.5 w-full rounded-full bg-[repeating-linear-gradient(45deg,var(--surface-2),var(--surface-2)_4px,var(--border)_4px,var(--border)_8px)]" title="未知" aria-label={label ?? "未知"} />
    );
  const bg = { brand: "bg-brand", warn: "bg-warn", bad: "bg-bad", ok: "bg-ok" }[tone];
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-2" role="progressbar" aria-valuenow={value} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className={cx("h-full rounded-full", bg)} style={{ width: `${Math.min(100, Math.max(0, value))}%` }} />
    </div>
  );
}

export function Empty({ icon, title, children }: { icon?: React.ReactNode; title: string; children?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center px-4 py-12 text-center">
      {icon && <div className="mb-3 text-faint">{icon}</div>}
      <div className="text-[14px] font-medium">{title}</div>
      {children && <div className="mt-1 max-w-md text-[12.5px] text-muted">{children}</div>}
    </div>
  );
}

/* ---------- Overlays ---------- */

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  wide,
}: {
  open: boolean;
  onClose: () => void;
  title: React.ReactNode;
  children: React.ReactNode;
  footer?: React.ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 p-0 sm:items-center sm:p-4" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        onClick={(e) => e.stopPropagation()}
        className={cx("flex max-h-[92vh] w-full flex-col rounded-t-xl border border-line bg-surface shadow-xl sm:rounded-xl", wide ? "sm:max-w-3xl" : "sm:max-w-lg")}
      >
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h3 className="text-[15px] font-semibold">{title}</h3>
          <button onClick={onClose} className="rounded p-1 text-muted hover:bg-surface-2" aria-label="关闭">
            <X size={16} />
          </button>
        </div>
        <div className="overflow-y-auto px-4 py-4">{children}</div>
        {footer && <div className="flex flex-wrap justify-end gap-2 border-t border-line px-4 py-3">{footer}</div>}
      </div>
    </div>
  );
}

export function Drawer({ open, onClose, title, children }: { open: boolean; onClose: () => void; title: React.ReactNode; children: React.ReactNode }) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 bg-black/40" onClick={onClose}>
      <aside
        role="dialog"
        aria-modal="true"
        onClick={(e) => e.stopPropagation()}
        className="absolute top-0 right-0 flex h-full w-full max-w-xl flex-col border-l border-line bg-surface shadow-xl"
      >
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h3 className="text-[15px] font-semibold">{title}</h3>
          <button onClick={onClose} className="rounded p-1 text-muted hover:bg-surface-2" aria-label="关闭">
            <X size={16} />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto p-4">{children}</div>
      </aside>
    </div>
  );
}

/** Confirmation dialog that requires typing a phrase for destructive ops. */
export function ConfirmModal({
  open,
  onClose,
  onConfirm,
  title,
  children,
  phrase,
  confirmLabel = "确认",
  danger,
  disabled,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void | boolean | Promise<void | boolean>;
  title: string;
  children: React.ReactNode;
  phrase?: string;
  confirmLabel?: string;
  danger?: boolean;
  disabled?: boolean;
}) {
  const [typed, setTyped] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submitting = useRef(false);
  useEffect(() => { setTyped(""); setError(null); }, [open, phrase]);
  const ok = !phrase || typed.trim() === phrase;
  return (
    <Modal
      open={open}
      onClose={() => { if (!submitting.current) onClose(); }}
      title={title}
      footer={
        <>
          <Button disabled={pending} onClick={() => { if (!submitting.current) onClose(); }}>取消</Button>
          <Button
            variant={danger ? "danger" : "primary"}
            disabled={!ok || pending || disabled || !!error}
            onClick={async () => {
              if (submitting.current || !ok) return;
              submitting.current = true; setPending(true); setError(null);
              try {
                if (await onConfirm() !== false) onClose();
              } catch { setError("操作结果尚未确认，请核对后再操作。"); }
              finally { submitting.current = false; setPending(false); }
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-[13px]">
        {children}
        {error && <Callout tone="warn">{error}</Callout>}
        {phrase && (
          <Field label={`请输入「${phrase}」以确认`}>
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} disabled={pending} autoFocus />
          </Field>
        )}
      </div>
    </Modal>
  );
}

export function Toolbar({ children }: { children: React.ReactNode }) {
  return <div className="mb-3 flex flex-wrap items-end gap-2">{children}</div>;
}

export function Mono({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-[12px]">{children}</span>;
}

export function Section({ title, children, desc }: { title: string; children: React.ReactNode; desc?: string }) {
  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-[13px] font-semibold">{title}</h3>
        {desc && <p className="text-[12px] text-muted">{desc}</p>}
      </div>
      {children}
    </div>
  );
}
