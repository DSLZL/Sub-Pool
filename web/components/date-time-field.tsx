"use client";

import { useState } from "react";
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
  const loading = displayTimezone === undefined;
  const zone = displayTimezone ?? "UTC";
  const mode = exact ? "UTC" : loading ? "读取时区中…" : zone;
  const localValue = exact || loading ? "" : pickerDateTime(value, zone);
  const invalid = !exact && !loading && !!value && (!localValue || !value.endsWith("Z"));

  return (
    <div className={cx("min-w-0", compact && "flex items-start gap-2", className)}>
      <Field
        label={compact ? label : label + "（" + mode + "）"}
        className={compact ? "min-w-0 flex-1" : undefined}
        hint={hint}
        error={invalid ? "请输入可唯一确定的时刻，或使用精确 UTC。" : undefined}
      >
        {exact ? (
          <Input value={value} maxLength={30} placeholder="YYYY-MM-DDTHH:mm:ssZ" disabled={disabled} onChange={event => onChange(event.target.value)} />
        ) : (
          <Input
            type="datetime-local"
            step={1}
            className="min-w-0 [color-scheme:light] dark:[color-scheme:dark]"
            value={localValue}
            title={value.endsWith("Z") ? value : undefined}
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
        aria-label={label + (exact ? "：使用日期选择" : "：输入精确 UTC")}
        disabled={disabled}
        onClick={() => setExact(current => !current)}
      >
        {exact ? "日期选择" : compact ? "高级" : "精确 UTC"}
      </button>
    </div>
  );
}
