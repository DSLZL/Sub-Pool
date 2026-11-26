"use client";

import { CircleHelp } from "lucide-react";
import { useEffect, useId, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";

export function UnknownValue({ reason }: { reason: string }) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const [position, setPosition] = useState<CSSProperties | null>(null);

  function show() {
    const bounds = trigger.current?.getBoundingClientRect();
    if (!bounds) return;
    const viewport = window.visualViewport;
    const width = Math.min(document.documentElement.clientWidth, viewport?.width ?? Infinity);
    const minLeft = (viewport?.offsetLeft ?? 0) + 8;
    const maxWidth = Math.min(240, width - 16);
    const left = Math.max(minLeft, Math.min(bounds.left, minLeft + width - maxWidth - 16));
    const above = bounds.top - (viewport?.offsetTop ?? 0) > 64;
    setPosition({
      left,
      maxWidth,
      top: above ? bounds.top - 4 : bounds.bottom + 4,
      transform: above ? "translateY(-100%)" : undefined,
    });
  }

  useEffect(() => {
    if (!position) return;
    const close = () => setPosition(null);
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !trigger.current?.contains(event.target)) close();
    };
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [position]);

  const tip = <span id={id} role="tooltip" style={position ?? undefined} className={position
    ? "pointer-events-none fixed z-[90] whitespace-normal rounded-md border border-line bg-surface px-2 py-1 text-xs text-ink shadow-[var(--shadow)]"
    : "sr-only"}>{reason}</span>;

  return <>
    <button ref={trigger} type="button" aria-label="未知" aria-describedby={id}
      className="relative inline-flex rounded p-0.5 text-unk align-middle hover:text-ink focus-visible:outline-2 focus-visible:outline-brand"
      onFocus={show} onBlur={() => setPosition(null)}
      onPointerEnter={event => { if (event.pointerType === "mouse") show(); }}
      onPointerLeave={() => { if (document.activeElement !== trigger.current) setPosition(null); }}
      onClick={event => { event.stopPropagation(); show(); }}
      onKeyDown={event => {
        if (event.key === "Escape" && position) {
          event.stopPropagation();
          setPosition(null);
        }
      }}>
      <CircleHelp size={12} aria-hidden />
      {!position && tip}
    </button>
    {position && createPortal(tip, document.body)}
  </>;
}
