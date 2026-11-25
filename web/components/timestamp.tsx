"use client";

import { displayTime } from "@/lib/connection-data";
import { useApp } from "@/lib/store";

export function Timestamp({ value, short = false, empty = "暂无记录", note, className }: {
  value: string | null | undefined;
  short?: boolean;
  empty?: string;
  note?: string;
  className?: string;
}) {
  const { displayTimezone } = useApp();
  if (!value) return <span className={className} title={note}>{empty}</span>;
  const title = [displayTime(value, displayTimezone), value, note].filter(Boolean).join("\n");
  return <time className={className} dateTime={value} title={title}>{displayTime(value, displayTimezone, short ? "short" : false)}</time>;
}
