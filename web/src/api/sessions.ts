import { requestJSON } from "./client";
import type { SessionDTO, SessionPageDTO } from "../types/api";

export const createSession = (userID: string, projectID: string) => requestJSON<SessionDTO>("/api/sessions", { method: "POST", body: JSON.stringify({ user_id: userID, project_id: projectID }) });
export const listSessions = (userID: string, projectID: string, before?: string) => {
  const query = new URLSearchParams({ user_id: userID, project_id: projectID, limit: "100" });
  if (before) query.set("before_session_id", before);
  return requestJSON<SessionPageDTO>(`/api/sessions?${query}`);
};

export async function listAllSessions(userID: string, projectID: string): Promise<SessionDTO[]> {
  const sessions: SessionDTO[] = [];
  let before: string | undefined;
  do {
    const page = await listSessions(userID, projectID, before);
    sessions.push(...page.sessions);
    before = page.has_more ? page.next_before_session_id : undefined;
  } while (before);
  return sessions;
}
