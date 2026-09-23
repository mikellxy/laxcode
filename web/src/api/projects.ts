import { requestJSON } from "./client";
import type { ProjectDTO, ProjectListDTO } from "../types/api";

export const createProject = (userID: string, name: string, workDir: string) =>
  requestJSON<ProjectDTO>("/api/projects", {
    method: "POST",
    body: JSON.stringify({ user_id: userID, name, work_dir: workDir }),
  });

export const listProjects = (userID: string) => {
  const query = new URLSearchParams({ user_id: userID });
  return requestJSON<ProjectListDTO>(`/api/projects?${query}`);
};
