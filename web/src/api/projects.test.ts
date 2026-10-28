import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, requestJSON } from "./client";
import { listProjectsWithDefault } from "./projects";

vi.mock("./client", async importOriginal => ({ ...await importOriginal<typeof import("./client")>(), requestJSON: vi.fn() }));
const request = vi.mocked(requestJSON);
const project = { project_id: "p1", name: "default", user_id: "user", work_dir: "/workplace/default", created_at: "", updated_at: "" };

describe("default project initialization", () => {
  beforeEach(() => request.mockReset());
  it("creates default only after successfully loading an empty list", async () => {
    request.mockResolvedValueOnce({ projects: [] }).mockResolvedValueOnce(project);
    expect(await listProjectsWithDefault("user")).toEqual({ projects: [project] });
    expect(request.mock.calls).toEqual([
      ["/api/projects?user_id=user"],
      ["/api/projects", { method: "POST", body: JSON.stringify({ user_id: "user", name: "default" }) }],
    ]);
  });
  it("does not create when projects already exist", async () => {
    request.mockResolvedValueOnce({ projects: [{ ...project, name: "existing" }] });
    await listProjectsWithDefault("user");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("does not treat list failure as an empty account", async () => {
    request.mockRejectedValueOnce(new ApiError("offline", 503));
    await expect(listProjectsWithDefault("user")).rejects.toThrow("offline");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("reuses default created by another tab", async () => {
    request.mockResolvedValueOnce({ projects: [] }).mockRejectedValueOnce(new ApiError("exists", 409)).mockResolvedValueOnce({ projects: [project] });
    expect(await listProjectsWithDefault("user")).toEqual({ projects: [project] });
    expect(request).toHaveBeenCalledTimes(3);
  });
  it("does not hide a conflict when no persisted default can be found", async () => {
    request.mockResolvedValueOnce({ projects: [] }).mockRejectedValueOnce(new ApiError("exists", 409)).mockResolvedValueOnce({ projects: [] });
    await expect(listProjectsWithDefault("user")).rejects.toThrow("exists");
  });
});
