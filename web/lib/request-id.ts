export const requestIdHeader = "X-Request-ID";
export const contractMismatchMessage = "前后端版本不一致，请检查部署。";

export function validRequestId(value: string | null) {
  return value?.length === 32 && /^[0-9a-f]+$/.test(value) ? value : null;
}

export function appendRequestId(message: string, id: string | null) {
  return id ? `${message} 请求编号 ${id}` : message;
}

export function requestFailureText(error: unknown, message: string) {
  const source = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const id = / 请求编号 ([0-9a-f]{32})$/.exec(source)?.[1];
  const hint = id && source.startsWith(contractMismatchMessage) && !message.startsWith(contractMismatchMessage) ? contractMismatchMessage : "";
  return appendRequestId(hint + message, id ?? null);
}
