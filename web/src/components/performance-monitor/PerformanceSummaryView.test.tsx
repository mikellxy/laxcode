import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { PerformanceSummary, RequestPerformance } from "../../types/api";
import { PerformanceSummaryView } from "./PerformanceSummaryView";
import { SessionTabs } from "../session-tabs/SessionTabs";

const empty: PerformanceSummary = { session_id: "session", chats: [], partial: false, invalid_line_count: 0, incomplete_trace_count: 0 };
const request: RequestPerformance = {
  trace_id: "trace-send", request_id: "send", operation: "send", start_time: "2026-10-16T04:00:00Z", end_time: "2026-10-16T04:00:01Z",
  status: "completed", total_ms: 1000, first_output_ms: 0,
  llm_generate: { count: 2, total_ms: 40, avg_ms: 20 }, tools: {},
};

describe("performance rendering", () => {
  it("shows weighted chat statistics and keeps send/resume first outputs separate", () => {
    const html = renderToStaticMarkup(<PerformanceSummaryView data={{ ...empty, chats: [{
      chat_id: "chat-one", total_ms: 1500, llm_generate: { count: 3, total_ms: 120, avg_ms: 40 },
      tools: { bash: { count: 1, total_ms: 0.123, avg_ms: 0.123 } },
      requests: [request, { ...request, trace_id: "trace-resume", request_id: "resume", operation: "resume", status: "error", first_output_ms: null, llm_generate: { count: 1, total_ms: 80, avg_ms: 80 } }],
    }] }} />);
    expect(html).toContain("1,500");
    expect(html).toContain("0 ms");
    expect(html).toContain("40 ms");
    expect(html).toContain("0.123");
    expect(html).toContain("bash");
    expect(html).toContain("恢复");
    expect(html).toContain("执行异常");
    expect(html).toContain("trace-resume");
    expect(html).toContain("<dd>—</dd>");
  });

  it("explains empty and partial logs without presenting an unfinished request as zero latency", () => {
    const html = renderToStaticMarkup(<PerformanceSummaryView data={{ ...empty, partial: true, invalid_line_count: 2, incomplete_trace_count: 1 }} />);
    expect(html).toContain("当前统计不完整");
    expect(html).toContain("2 行日志无效，1 条 Trace");
    expect(html).toContain("暂无已完成的请求记录");
    expect(html).not.toContain("0 ms");
  });

  it("places the trigger below Context and disables it without a session", () => {
    const renderSidebar = (selectedID?: string) => renderToStaticMarkup(<SessionTabs
      selectedID={selectedID} projects={[]} sessionsByProject={{}} creatingProject={false} runningSessionIDs={[]}
      onSelect={() => {}} onSelectProject={() => {}} onDeleteSession={() => {}} onCreateProject={() => {}} onCreateSession={() => {}}
    />);
    const cloud = renderSidebar("session");
    expect(cloud.indexOf("Context")).toBeLessThan(cloud.indexOf("性能监控"));
    expect(cloud.indexOf("性能监控")).toBeLessThan(cloud.indexOf("服务连接就绪"));
    expect(cloud).not.toContain('class="performance-trigger" disabled');
    expect(renderSidebar()).toContain('class="performance-trigger" disabled');
  });
});
