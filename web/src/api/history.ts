import { request } from "./client";
import { ApiError, requestJSON } from "./client";
import type { HistoryPageDTO } from "../types/api";

export const listHistory = (sessionID: string, before?: number) => {
  const query = new URLSearchParams({ limit: "50" });
  if (before) query.set("before_seq", String(before));
  return requestJSON<HistoryPageDTO>(`/api/sessions/${encodeURIComponent(sessionID)}/messages?${query}`);
};

export async function downloadChatFiles(sessionID: string, chatID: string, path?: string): Promise<{ blob: Blob; filename: string }> {
  const base = `/api/sessions/${encodeURIComponent(sessionID)}/chats/${encodeURIComponent(chatID)}`;
  const response = await request(path === undefined ? `${base}/files.zip` : `${base}/file?${new URLSearchParams({ path, download: "1" })}`);
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { message?: string } | null;
    throw new ApiError(payload?.message || `下载失败 (${response.status})`, response.status);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const encoded = /filename\*=utf-8''([^;]+)/i.exec(disposition)?.[1];
  const plain = /filename="((?:[^"\\]|\\.)*)"|filename=([^;]+)/i.exec(disposition);
  let filename = plain?.[1]?.replace(/\\(.)/g, "$1") ?? plain?.[2]?.trim() ?? path?.split("/").pop() ?? "chat-files.zip";
  if (encoded) {
    try { filename = decodeURIComponent(encoded); } catch { /* Use the fallback for a malformed header. */ }
  }
  return { blob: await response.blob(), filename };
}

export type ChatFilePreview = { kind: "text"; content: string; truncated: boolean } | { kind: "image"; blob: Blob };

export async function readChatFile(sessionID: string, chatID: string, path: string, signal: AbortSignal): Promise<ChatFilePreview> {
  const query = new URLSearchParams({ path });
  const response = await request(`/api/sessions/${encodeURIComponent(sessionID)}/chats/${encodeURIComponent(chatID)}/file?${query}`, { signal, cache: "no-store" });
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { message?: string } | null;
    throw new ApiError(payload?.message || `预览失败 (${response.status})`, response.status);
  }
  const type = response.headers.get("Content-Type")?.split(";")[0].trim();
  if (type === "text/plain") return { kind: "text", content: await response.text(), truncated: response.headers.get("X-Preview-Truncated") === "true" };
  if (["image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp"].includes(type ?? "")) return { kind: "image", blob: await response.blob() };
  throw new ApiError("暂不支持预览此文件类型，请下载后查看", 415);
}
