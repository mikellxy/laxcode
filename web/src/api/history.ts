import { requestJSON } from "./client";
import type { HistoryPageDTO } from "../types/api";

export const listHistory = (sessionID: string, before?: number) => {
  const query = new URLSearchParams({ limit: "50" });
  if (before) query.set("before_seq", String(before));
  return requestJSON<HistoryPageDTO>(`/api/sessions/${encodeURIComponent(sessionID)}/messages?${query}`);
};
