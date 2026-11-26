"use client";

import { useId, useState } from "react";
import { cx, Field, Input } from "@/components/ui";
import { pickerDateTime, pickerTimeToUTC } from "@/lib/date-time-input";
import { useApp } from "@/lib/store";

export function DateTimeField({ label, value, onChange, className, disabled, hint, compact = false }: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  className?: string;
  disabled?: boolean;
  hint?: string;
  compact?: boolean;
}) {
  const { displayTimezone } = useApp();
  const [exact, setExact] = useState(false);
  const feedbackId = useId();
  const loading = displayTimezone === undefined;
  const zone = displayTimezone ?? "UTC";
  const zoneHint = displayTimezone ? "统计时区 " + displayTimezone : "时区待确认（UTC）";
  const formatHint = exact ? "精确 UTC · 例 2026-11-20T14:23:00Z"
    : loading ? "读取时区…" : zoneHint + " · 例 2026-11-20 14:23:00";
  const localValue = exact || loading ? "" : pickerDateTime(value, zone);
  const invalid = !exact && !loading && !!value && (!localValue || !value.endsWith("Z"));
  const feedback = invalid ? "选时间，或输入 UTC。"
    : formatHint + (hint ? " · " + hint : "");

  return (
    <div className={cx("min-w-0", compact && "flex items-start gap-2", className)}>
      <Field
        label={label}
        className={compact ? "min-w-0 flex-1" : undefined}
        hint={<span id={feedbackId} className={invalid ? "text-bad" : undefined}>{feedback}</span>}
      >
        {exact ? (
          <Input aria-label={label} aria-describedby={feedbackId} value={value} maxLength={30}
            placeholder="YYYY-MM-DDTHH:mm:ssZ" disabled={disabled} onChange={event => onChange(event.target.value)} />
        ) : (
          <Input
            type="datetime-local"
            step={1}
            className="min-w-0 [color-scheme:light] dark:[color-scheme:dark]"
            value={localValue}
            title={value.endsWith("Z") ? value : undefined}
            aria-label={label}
            aria-describedby={feedbackId}
            aria-invalid={invalid || undefined}
            disabled={disabled || loading}
            onChange={event => {
              const local = event.target.value;
              // Keep an unresolved wall time invalid for the existing UTC validators.
              onChange(local ? pickerTimeToUTC(local, zone) ?? local : "");
            }}
          />
        )}
      </Field>
      <button
        type="button"
        className={cx(compact ? "w-12 shrink-0 text-right" : "mt-1", "text-[11.5px] text-brand hover:underline disabled:cursor-not-allowed disabled:text-muted")}
        aria-label={label + (exact ? "：使用日期选择" : "：高级时间设置")}
        disabled={disabled}
        onClick={() => setExact(current => !current)}
      >
        {exact ? "日期选择" : "高级"}
      </button>
    </div>
  );
}
