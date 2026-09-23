import { describe, expect, it } from "vitest";
import { historyToMessage } from "./chat";

describe("historyToMessage", () => {
  it("restores reasoning as a completed, collapsed block", () => {
    const message = historyToMessage({
      seq: 8,
      role: "assistant",
      content: "answer",
      reasoning_content: "thinking",
      created_at: "2026-09-23T06:00:00Z",
    });

    expect(message).toMatchObject({
      content: "answer",
      reasoningContent: "thinking",
      reasoningStreaming: false,
      status: "completed",
    });
  });
});
