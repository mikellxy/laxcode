import { request } from "./client";
import { ApiError, requestJSON } from "./client";
import type { EvaluationJobDTO, EvaluationJobListDTO } from "../types/api";

export const createEvaluation = (sessionID: string, requirement: string) =>
  requestJSON<EvaluationJobDTO>("/api/evaluations", {
    method: "POST",
    body: JSON.stringify({ session_id: sessionID, requirement }),
  });

export const listEvaluationJobs = (sessionID: string) => {
  const query = new URLSearchParams({ session_id: sessionID });
  return requestJSON<EvaluationJobListDTO>(`/api/evaluations?${query}`);
};

export async function downloadEvaluationReport(sessionID: string, jobID: string): Promise<Blob> {
  const response = await request(`/api/sessions/${encodeURIComponent(sessionID)}/evaluations/${encodeURIComponent(jobID)}/report`, { cache: "no-store" });
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { message?: string } | null;
    throw new ApiError(payload?.message || `报告下载失败 (${response.status})`, response.status);
  }
  return response.blob();
}
