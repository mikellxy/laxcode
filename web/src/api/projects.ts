import { ApiError, requestJSON } from "./client";
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

export async function listProjectsWithDefault(userID: string): Promise<ProjectListDTO> {
  const listed = await listProjects(userID);
  if (listed.projects.length > 0) return listed;
  try {
    return { projects: [await createProject(userID, "default")] };
  } catch (error) {
    // Another tab may have created the same project after our empty-list response.
    if (error instanceof ApiError && error.status === 409) {
      const refreshed = await listProjects(userID);
      if (refreshed.projects.some(project => project.name === "default")) return refreshed;
    }
    throw error;
  }
}
