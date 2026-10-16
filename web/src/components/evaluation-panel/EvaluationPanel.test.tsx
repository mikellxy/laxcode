import { describe, expect, it } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { EvaluationPanel } from "./EvaluationPanel";

function render(sessionID?: string, workDir?: string) {
  return renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}>
    <EvaluationPanel sessionID={sessionID} workDir={workDir} disabled={false} />
  </QueryClientProvider>);
}

describe("evaluation entrance", () => {
  it("keeps the entrance visible for a new conversation and explains why it is disabled", () => {
    const html = render(undefined, "/demo");
    expect(html).toContain("<span>评估</span>");
    expect(html).toContain('disabled=""');
    expect(html).toContain("发送首条消息后即可评估");
    expect(html).not.toContain('class="evaluation-panel"');
  });

  it("enables evaluation for an existing session", () => {
    const html = render("session", "/demo");
    expect(html).toContain('aria-label="评估当前会话"');
    expect(html).not.toContain('disabled=""');
  });

  it("omits evaluation before a project is selected", () => {
    expect(render()).toBe("");
  });
});
