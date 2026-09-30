import { afterEach, describe, expect, it, vi } from "vitest";
import { createEvaluation, listEvaluationJobs } from "./evaluations";

afterEach(() => vi.unstubAllGlobals());

describe("evaluation API", () => {
  it("creates a job with the selected session context", async () => {
    const job = { id: "eval_s1_now", source_session_id: "s1", requirement: "check tests", status: "queued", created_at: "2026-09-27T00:00:00Z", updated_at: "2026-09-27T00:00:00Z" };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(job), { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(createEvaluation("s1", "/projects/demo", "check tests")).resolves.toEqual(job);
    expect(fetchMock).toHaveBeenCalledWith("/api/evaluations", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ session_id: "s1", work_dir: "/projects/demo", requirement: "check tests" }),
    }));
  });

  it("lists jobs for one source session", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ jobs: [] }), { status: 200 })));
    await expect(listEvaluationJobs("session with spaces")).resolves.toEqual({ jobs: [] });
    expect(fetch).toHaveBeenCalledWith("/api/evaluations?session_id=session+with+spaces", expect.any(Object));
  });
});
