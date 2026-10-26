import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MessageList } from "./MessageList";
import type { StreamState } from "../../features/chat/reducer";
import type { ChatMessage } from "../../types/chat";
import type { RetryAction } from "../../types/api";

vi.mock("react", async (original) => ({ ...await original<typeof import("react")>(), useLayoutEffect: vi.fn() }));

function render(waitingFor?: StreamState["waitingFor"], retrying = true, error?: string, messages: ChatMessage[] = [], retryAction?: RetryAction) {
  return renderToStaticMarkup(<MessageList messages={messages} hasMore={false} loading={false} initialLoading={false} waitingFor={waitingFor} retrying={retrying} error={error} retryAction={retryAction} onRetry={() => {}} onLoadMore={async () => {}} />);
}

describe("sandbox preparation status", () => {
  it("places the waiting status immediately after the last user, before existing replies", () => {
    const messages: ChatMessage[] = [
      { id: "u1", role: "user", content: "older user", reasoningStreaming: false, createdAt: "2026-10-26T00:00:00Z", status: "completed" },
      { id: "u2", role: "user", content: "latest user", reasoningStreaming: false, createdAt: "2026-10-26T00:00:00Z", status: "completed" },
      { id: "a", role: "assistant", content: "existing reply", reasoningStreaming: false, createdAt: "2026-10-26T00:00:00Z", status: "completed" },
    ];
    const html = render("sandbox", true, undefined, messages);
    expect(html.indexOf('role="status"')).toBeGreaterThan(html.indexOf("latest user"));
    expect(html.indexOf('role="status"')).toBeLessThan(html.indexOf("existing reply"));
    expect(html.match(/role="status"/g)).toHaveLength(1);
  });
  it("offers a single resume button without requiring an error, and hides it while running", () => {
    expect(render(undefined, false, undefined, [], "resume")).toContain('class="resume-action"');
    expect(render(undefined, false, "连接中断", [], "resume").match(/继续执行/g)).toHaveLength(1);
    expect(render("sandbox", true, undefined, [], "resume")).not.toContain("继续执行");
    expect(render(undefined, false)).not.toContain("继续执行");
  });
  it.each([
    ["sandbox", "正在进入您的专属安全沙箱…"],
    ["reply", "已进入您的专属安全沙箱，正在准备回复…"],
  ] as const)("shows %s waiting with the same animated dots", (phase, message) => {
    const html = render(phase);
    expect(html).toContain(message);
    expect(html).toContain('role="status"');
    expect(html).toContain('<span class="typing" aria-hidden="true"><i></i><i></i><i></i></span>');
    expect(html).not.toContain("从一个想法开始");
  });
  it("hides the status after output, completion, and errors", () => {
    expect(render()).not.toContain('role="status"');
    expect(render("reply", false)).not.toContain('role="status"');
    expect(render("sandbox", true, "启动失败")).not.toContain('role="status"');
  });
  it("keeps only one loading animation when initial output is whitespace", () => {
    const html = render("reply", true, undefined, [{ id: "a", role: "assistant", content: " \n", reasoningStreaming: false, createdAt: new Date().toISOString(), status: "streaming" }]);
    expect(html.match(/class="typing"/g)).toHaveLength(1);
    expect(html).not.toContain('class="message assistant"');
  });
});
