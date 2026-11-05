"use client";

import { useState } from "react";

/** Multi-series line chart. null values break the line (gap) — never drawn as zero. */
export function LineChart({
  labels,
  series,
  height = 260,
  unit = "",
}: {
  labels: string[];
  series: { name: string; color: string; values: (number | null)[] }[];
  height?: number;
  unit?: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 960;
  const H = height;
  const pad = { l: 36, r: 12, t: 12, b: 24 };
  const all = series.flatMap((s) => s.values.filter((v): v is number => v !== null));
  const max = Math.max(1, ...all) * 1.15;
  const x = (i: number) => pad.l + (i * (W - pad.l - pad.r)) / Math.max(1, labels.length - 1);
  const y = (v: number) => pad.t + (1 - v / max) * (H - pad.t - pad.b);
  const ticks = [0, max / 2, max];
  const gaps = labels.map((_, i) => series.every((s) => s.values[i] === null));

  return (
    <div className="relative w-full">
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label="趋势图">
        {ticks.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeDasharray={t === 0 ? "" : "3 3"} />
            <text x={pad.l - 6} y={y(t) + 3} textAnchor="end" fontSize="10" fill="var(--faint)">
              {t.toFixed(t < 10 ? 1 : 0)}
            </text>
          </g>
        ))}
        {gaps.map(
          (g, i) =>
            g && (
              <g key={`gap${i}`}>
                <rect x={x(i) - 10} y={pad.t} width={20} height={H - pad.t - pad.b} fill="var(--unk-soft)" opacity={0.8} />
                <text x={x(i)} y={pad.t + 12} textAnchor="middle" fontSize="9" fill="var(--unk)">
                  缺口
                </text>
              </g>
            ),
        )}
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
              {s.values.map((v, i) => v !== null && hover === i && <circle key={i} cx={x(i)} cy={y(v)} r={3.5} fill={s.color} />)}
            </g>
          );
        })}
        {labels.map((l, i) => (
          <text key={l} x={x(i)} y={H - 6} textAnchor="middle" fontSize="10" fill="var(--faint)">
            {i % 2 === 0 || labels.length < 8 ? l : ""}
          </text>
        ))}
        {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={H - pad.b} stroke="var(--faint)" strokeDasharray="2 2" />}
        {labels.map((_, i) => (
          <rect
            key={`h${i}`}
            x={x(i) - (W - pad.l - pad.r) / labels.length / 2}
            y={0}
            width={(W - pad.l - pad.r) / labels.length}
            height={H}
            fill="transparent"
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </svg>
      {hover !== null && (
        <div className="pointer-events-none absolute top-1 right-1 rounded-md border border-line bg-surface px-2.5 py-1.5 text-[12px] shadow-md">
          <div className="mb-1 font-medium">{labels[hover]}</div>
          {series.map((s) => (
            <div key={s.name} className="flex items-center gap-2">
              <span className="h-2 w-2 rounded-full" style={{ background: s.color }} />
              <span className="text-muted">{s.name}</span>
              <span className="num ml-auto pl-3">{s.values[hover] === null ? <span className="text-unk">未知</span> : `${s.values[hover]!.toFixed(2)}${unit}`}</span>
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
          无记录（不等于 0）
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
export function ShareBar({ parts }: { parts: { label: string; pct: number; color: string }[] }) {
  const used = parts.reduce((a, b) => a + b.pct, 0);
  return (
    <div>
      <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-surface-2">
        {parts.map((p) => (
          <div key={p.label} style={{ width: `${p.pct}%`, background: p.color }} title={`${p.label} ${p.pct}%`} />
        ))}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[12px] text-muted">
        {parts.map((p) => (
          <span key={p.label} className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: p.color }} />
            {p.label} <span className="num text-ink">{p.pct}%</span>
          </span>
        ))}
        {used < 100 && (
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full bg-line" />
            未分配 <span className="num text-ink">{100 - used}%</span>
          </span>
        )}
      </div>
    </div>
  );
}

export const CHART_COLORS = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)", "var(--chart-4)"];
