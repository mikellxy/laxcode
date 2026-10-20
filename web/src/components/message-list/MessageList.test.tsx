import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MessageList } from "./MessageList";
import type { StreamState } from "../../features/chat/reducer";
import type { ChatMessage } from "../../types/chat";

vi.mock("react", async (original) => ({ ...await original<typeof import("react")>(), useLayoutEffect: vi.fn() }));

function render(waitingFor?: StreamState["waitingFor"], retrying = true, error?: string, messages: ChatMessage[] = []) {
  return renderToStaticMarkup(<MessageList messages={messages} hasMore={false} loading={false} initialLoading={false} waitingFor={waitingFor} retrying={retrying} error={error} onRetry={() => {}} onLoadMore={async () => {}} />);
}

describe("sandbox preparation status", () => {
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
