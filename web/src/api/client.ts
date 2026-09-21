export class ApiError extends Error {
  constructor(message: string, public readonly status: number, public readonly code?: string, public readonly retryAction?: "resume" | "resend") { super(message); this.name = "ApiError"; }
}

export async function requestJSON<T>(input: RequestInfo | URL, init?: RequestInit): Promise<T> {
  const response = await fetch(input, { ...init, headers: { Accept: "application/json", ...(init?.body ? { "Content-Type": "application/json" } : {}), ...init?.headers } });
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { message?: string; code?: string; retry_action?: "resume" | "resend" } | null;
    throw new ApiError(payload?.message || `请求失败 (${response.status})`, response.status, payload?.code, payload?.retry_action);
  }
  return response.json() as Promise<T>;
}
