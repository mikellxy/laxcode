import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { EvaluationJobDTO } from "../../types/api";
import { EvaluationReportDownload } from "./EvaluationReportDownload";

const job: EvaluationJobDTO = {
  id: "eval_session_now", source_session_id: "session", requirement: "check task", status: "succeeded",
  created_at: "2026-10-16T00:00:00Z", updated_at: "2026-10-16T00:01:00Z",
};

describe("evaluation report download", () => {
  it("offers successful cloud jobs a download without requiring a report_path", () => {
    const html = renderToStaticMarkup(<EvaluationReportDownload sessionID="session" job={job} />);
    expect(html).toContain("下载评估报告");
    expect(html).not.toContain("disabled");
  });

  it("never renders a host report path, including a legacy local-mode path", () => {
    const html = renderToStaticMarkup(<EvaluationReportDownload sessionID="session" job={{ ...job, report_path: "/home/host/.laxcode/eval/private.md" }} />);
    expect(html).not.toContain("/home/host");
    expect(html).toContain("下载评估报告");
  });

  it.each(["queued", "running", "failed"] as const)("does not offer a download for %s jobs", (status) => {
    expect(renderToStaticMarkup(<EvaluationReportDownload sessionID="session" job={{ ...job, status }} />)).toBe("");
  });
});
