import { requestJSON } from "./client";
import type { ContextData } from "../types/api";

export const getSessionContext = (sessionID: string) => requestJSON<ContextData>(`/api/sessions/${encodeURIComponent(sessionID)}/context`);
