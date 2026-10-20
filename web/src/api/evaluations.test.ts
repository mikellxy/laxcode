import { afterEach, describe, expect, it, vi } from "vitest";
import { createEvaluation, downloadEvaluationReport, listEvaluationJobs } from "./evaluations";

afterEach(() => vi.unstubAllGlobals());

describe("evaluation API", () => {
  it("creates a job with the selected session context", async () => {
    const job = { id: "eval_s1_now", source_session_id: "s1", requirement: "check tests", status: "queued", created_at: "2026-09-27T00:00:00Z", updated_at: "2026-09-27T00:00:00Z" };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(job), { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(createEvaluation("s1", "check tests")).resolves.toEqual(job);
    expect(fetchMock).toHaveBeenCalledWith("/api/evaluations", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ session_id: "s1", requirement: "check tests" }),
    }));
  });

  it("lists jobs for one source session", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ jobs: [] }), { status: 200 })));
    await expect(listEvaluationJobs("session with spaces")).resolves.toEqual({ jobs: [] });
    expect(fetch).toHaveBeenCalledWith("/api/evaluations?session_id=session+with+spaces", expect.any(Object));
  });

  it("downloads complete Markdown by session and job ID, without passing a file path", async () => {
    const markdown = "# 评估报告\n" + "完整报告\n".repeat(60000);
    const fetchMock = vi.fn(async () => new Response(markdown, { headers: { "Content-Type": "text/markdown; charset=utf-8" } }));
    vi.stubGlobal("fetch", fetchMock);
    const blob = await downloadEvaluationReport("session/one", "eval job");
    expect(await blob.text()).toBe(markdown);
    expect(fetchMock).toHaveBeenCalledWith("/api/sessions/session%2Fone/evaluations/eval%20job/report", expect.objectContaining({ cache: "no-store", headers: { "X-USERID": "11111111-1111-4111-8111-111111111111" } }));
  });

  it("reports missing and unfinished reports as errors instead of downloading the error body", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ message: "evaluation report is not ready" }), { status: 409 })));
    await expect(downloadEvaluationReport("session", "job")).rejects.toMatchObject({ status: 409, message: "evaluation report is not ready" });
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not found", { status: 404 })));
    await expect(downloadEvaluationReport("session", "job")).rejects.toMatchObject({ status: 404, message: "报告下载失败 (404)" });
  });
});

vi.mock("../features/identity/guest", () => ({ getOrCreateGuestID: () => "11111111-1111-4111-8111-111111111111" }));
