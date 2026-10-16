import { requestJSON } from "./client";
import type { ProjectDTO, ProjectListDTO } from "../types/api";

export const createProject = (userID: string, name: string) =>
  requestJSON<ProjectDTO>("/api/projects", {
    method: "POST",
    body: JSON.stringify({ user_id: userID, name }),
  });

export const listProjects = (userID: string) => {
  const query = new URLSearchParams({ user_id: userID });
  return requestJSON<ProjectListDTO>(`/api/projects?${query}`);
};
