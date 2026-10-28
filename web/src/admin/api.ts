export class AdminAPIError extends Error {
  constructor(message: string, public readonly status: number) { super(message); }
}

export async function adminRequest<T>(path: string, token?: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (init?.body) headers.set("Content-Type", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(`/admin/api${path}`, { ...init, headers, cache: "no-store", credentials: "omit" });
  const data = await response.json().catch(() => null) as T & { message?: string } | null;
  if (!response.ok) throw new AdminAPIError(data?.message || `请求失败 (${response.status})`, response.status);
  if (!data) throw new Error("服务器返回了无效数据");
  return data;
}

export type AdminUserPage = { users: { user_id: string }[]; has_more: boolean; next_after_user_id?: string };
export const isAdminPath = (path: string) => path === "/admin" || path.startsWith("/admin/");
