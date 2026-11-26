"use client";

import { uiCopy } from "@/lib/glossary";
import { useEffect, useId, useRef, useState } from "react";

/** Multi-series line chart. null values break the line (gap) — never drawn as zero. */
export function LineChart({
  labels,
  series,
  height = 260,
  unit = "",
}: {
  labels: string[];
  series: { name: string; color: string; values: (number | string | null)[] }[];
  height?: number;
  unit?: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const [W, setWidth] = useState(960);
  const container = useRef<HTMLDivElement>(null);
  const description = useId();
  useEffect(() => {
    const node = container.current;
    if (!node) return;
    const observer = new ResizeObserver(([entry]) => {
      if (entry && entry.contentRect.width > 0) setWidth(entry.contentRect.width);
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  const H = height;
  // Exact strings are retained for hover and the chart description.
  const all = series.flatMap((s) => s.values.filter(v => v !== null).map(Number));
  const max = all.reduce((largest, value) => Math.max(largest, value), 0) * 1.15;
  const ticks = (max === 0 ? [0] : [0, max / 2, max]).map(value => ({
    value,
    label: value >= 10 ? value.toFixed(0) : String(Number(value.toPrecision(2))),
  }));
  const dateWidth = labels.reduce((width, label) => Math.max(width, label.length * 7 + 8), 0);
  const pad = { l: Math.max(36, dateWidth / 2, ...ticks.map(tick => tick.label.length * 8 + 12)), r: Math.max(12, dateWidth / 2), t: 12, b: 24 };
  const plotWidth = Math.max(1, W - pad.l - pad.r), last = labels.length - 1;
  const step = plotWidth / Math.max(1, last);
  const x = (i: number) => pad.l + (last === 0 ? plotWidth / 2 : i * step);
  const y = (v: number | string) => pad.t + (1 - Number(v) / (max || 1)) * (H - pad.t - pad.b);
  const dayLeft = (i: number) => i === 0 ? pad.l : x(i) - step / 2;
  const dayRight = (i: number) => i === last ? pad.l + plotWidth : x(i) + step / 2;
  const labelStep = Math.max(1, Math.ceil(dateWidth / step));
  const gaps: { start: number; end: number }[] = [];
  labels.forEach((_, i) => {
    if (!series.every(s => s.values[i] === null)) return;
    const previous = gaps.at(-1);
    if (previous && previous.end === i - 1) previous.end = i;
    else gaps.push({ start: i, end: i });
  });

  return (
    <div className="relative w-full" ref={container}>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label="趋势图" aria-describedby={description}>
        <desc id={description}>{series.map(s => `${s.name}：${s.values.map((value, i) => `${labels[i]} ${value === null ? "未知" : `${value}${unit}`}`).join("；")}`).join("\n")}</desc>
        {ticks.map(({ value, label }) => (
          <g key={value}>
            <line x1={pad.l} x2={pad.l + plotWidth} y1={y(value)} y2={y(value)} stroke="var(--border)" strokeDasharray={value === 0 ? "" : "3 3"} />
            <text x={pad.l - 6} y={y(value) + 3} textAnchor="end" fontSize="12" fill="var(--faint)">
              {label}
            </text>
          </g>
        ))}
        {gaps.map(({ start, end }) => {
          const left = dayLeft(start), right = dayRight(end);
          return <g key={start}>
            <rect x={left} y={pad.t} width={right - left} height={H - pad.t - pad.b} fill="var(--unk-soft)" opacity={0.8} />
            {right - left >= 18 && <text x={(left + right) / 2} y={pad.t + 12} textAnchor="middle" fontSize="9" fill="var(--unk)">缺口</text>}
          </g>;
        })}
        {series.map((s) => {
          const segs: string[] = [];
          let cur = "";
          s.values.forEach((v, i) => {
            if (v === null) {
              if (cur) segs.push(cur);
              cur = "";
            } else cur += `${cur ? "L" : "M"}${x(i)},${y(v)} `;
          });
          if (cur) segs.push(cur);
          return (
            <g key={s.name}>
              {segs.map((d, k) => (
                <path key={k} d={d} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
              ))}
              {s.values.map((v, i) => v !== null && (hover === i || (s.values[i - 1] == null && s.values[i + 1] == null)) &&
                <circle key={i} cx={x(i)} cy={y(v)} r={3.5} fill={s.color} />)}
            </g>
          );
        })}
        {labels.map((l, i) => (i === last || (i % labelStep === 0 && last - i >= labelStep)) && (
          <text key={l} x={x(i)} y={H - 6} textAnchor="middle" fontSize="12" fill="var(--faint)">
            {l}
          </text>
        ))}
        {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={H - pad.b} stroke="var(--faint)" strokeDasharray="2 2" />}
        {labels.map((_, i) => (
          <rect
            key={`h${i}`}
            x={dayLeft(i)}
            y={0}
            width={dayRight(i) - dayLeft(i)}
            height={H}
            fill="transparent"
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </svg>
      {hover !== null && (
        <div className="pointer-events-none absolute top-1 right-1 max-w-full rounded-md border border-line bg-surface px-2.5 py-1.5 text-[12px] shadow-md [overflow-wrap:anywhere]">
          <div className="mb-1 font-medium">{labels[hover]}</div>
          {series.map((s) => (
            <div key={s.name} className="flex items-center gap-2">
              <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: s.color }} />
              <span className="text-muted">{s.name}</span>
              <span className="num ml-auto pl-3">{s.values[hover] === null ? <span className="text-unk">未知</span> : `${typeof s.values[hover] === "number" ? s.values[hover].toFixed(2) : s.values[hover]}${unit}`}</span>
            </div>
          ))}
        </div>
      )}
      <div className="mt-2 flex flex-wrap gap-3 text-[12px] text-muted">
        {series.map((s) => (
          <span key={s.name} className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: s.color }} />
            {s.name}
          </span>
        ))}
        <span className="inline-flex items-center gap-1.5">
          <span className="h-2 w-3 rounded-sm bg-unk-soft" />
          {uiCopy.noRecords}
        </span>
      </div>
    </div>
  );
}

/** Horizontal range bar for estimate intervals. */
export function RangeBar({ low, mid, high, max }: { low: number; mid: number; high: number; max: number }) {
  const p = (v: number) => `${(v / max) * 100}%`;
  return (
    <div className="relative h-3 w-full rounded-full bg-surface-2">
      <div className="absolute top-0 h-3 rounded-full bg-brand/25" style={{ left: p(low), width: `calc(${p(high)} - ${p(low)})` }} />
      <div className="absolute top-[-2px] h-4 w-0.5 rounded bg-brand" style={{ left: p(mid) }} />
    </div>
  );
}

/** Share composition stacked bar. */
export function ShareBar({ parts, unallocated }: { parts: { id: string; label: string; pct: string; color: string }[]; unallocated: string | null }) {
  return (
    <div>
      <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-surface-2">
        {parts.map((p) => (
          <div key={p.id} style={{ width: `${p.pct}%`, background: p.color }} title={`${p.label} ${p.pct}%`} />
        ))}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[12px] text-muted">
        {parts.map((p) => (
          <span key={p.id} className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: p.color }} />
            {p.label} <span className="num text-ink">{p.pct}%</span>
          </span>
        ))}
        {unallocated !== null && unallocated !== "0" && (
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full bg-line" />
            未分配 <span className="num text-ink">{unallocated}%</span>
          </span>
        )}
      </div>
    </div>
  );
}

export const CHART_COLORS = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)", "var(--chart-4)"];
