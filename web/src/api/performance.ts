import { requestJSON } from "./client";
import type { PerformanceSummary } from "../types/api";

export const getSessionPerformance = (sessionID: string, signal?: AbortSignal) =>
  requestJSON<PerformanceSummary>(`/api/sessions/${encodeURIComponent(sessionID)}/performance`, { signal, cache: "no-store" });
