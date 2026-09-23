import { requestJSON } from "./client";
import type { SessionDTO, SessionPageDTO } from "../types/api";

export const createSession = (userID: string, workDir: string) => requestJSON<SessionDTO>("/api/sessions", { method: "POST", body: JSON.stringify({ user_id: userID, work_dir: workDir }) });
export const listSessions = (userID: string, before?: string) => {
  const query = new URLSearchParams({ user_id: userID, limit: "20" });
  if (before) query.set("before_session_id", before);
  return requestJSON<SessionPageDTO>(`/api/sessions?${query}`);
};
