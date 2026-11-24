export function CursorNavigation({ after, next, first = "", onChange, disabled = false }: {
  after: string;
  next: string | null;
  first?: string;
  onChange: (after: string) => void;
  disabled?: boolean;
}) {
  if (after === first && next === null) return null;
  return <div className="flex flex-wrap gap-2 px-4 py-3">
    {after !== first && <button type="button" className="text-xs underline" disabled={disabled} onClick={() => onChange(first)}>回到首页</button>}
    {next !== null && <button type="button" className="text-xs underline" disabled={disabled} onClick={() => onChange(next)}>下一页</button>}
  </div>;
}
