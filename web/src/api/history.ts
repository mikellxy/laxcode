import { ApiError, requestJSON } from "./client";
import type { HistoryPageDTO } from "../types/api";

export const listHistory = (sessionID: string, before?: number) => {
  const query = new URLSearchParams({ limit: "50" });
  if (before) query.set("before_seq", String(before));
  return requestJSON<HistoryPageDTO>(`/api/sessions/${encodeURIComponent(sessionID)}/messages?${query}`);
};

export async function downloadChatFiles(sessionID: string, chatID: string): Promise<Blob> {
  const response = await fetch(`/api/sessions/${encodeURIComponent(sessionID)}/chats/${encodeURIComponent(chatID)}/files.zip`);
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { message?: string } | null;
    throw new ApiError(payload?.message || `下载失败 (${response.status})`, response.status);
  }
  return response.blob();
}
