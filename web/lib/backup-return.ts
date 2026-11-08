// Only a public correlation ID survives fresh login; it never supplies authority.
export function restorePlanID(search: string): string | null {
  const params = new URLSearchParams(search), values = params.getAll("restore");
  return values.length === 1 && /^[A-Za-z0-9_-]{21}[AQgw]$/.test(values[0] ?? "") ? values[0] ?? null : null;
}
