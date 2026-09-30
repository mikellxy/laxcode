import { requestJSON } from "./client";
import type { EvaluationJobDTO, EvaluationJobListDTO } from "../types/api";

export const createEvaluation = (sessionID: string, workDir: string, requirement: string) =>
  requestJSON<EvaluationJobDTO>("/api/evaluations", {
    method: "POST",
    body: JSON.stringify({ session_id: sessionID, work_dir: workDir, requirement }),
  });

export const listEvaluationJobs = (sessionID: string) => {
  const query = new URLSearchParams({ session_id: sessionID });
  return requestJSON<EvaluationJobListDTO>(`/api/evaluations?${query}`);
};
