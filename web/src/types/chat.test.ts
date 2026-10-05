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

  it("carries chat_id on user messages and leaves it undefined when absent", () => {
    const withId = historyToMessage({
      seq: 1,
      role: "user",
      content: "question",
      chat_id: "chat-uuid-1",
      created_at: "2026-09-23T06:00:00Z",
    });
    expect(withId.chatId).toBe("chat-uuid-1");

    const withoutId = historyToMessage({
      seq: 2,
      role: "user",
      content: "legacy question",
      created_at: "2026-09-23T06:01:00Z",
    });
    expect(withoutId.chatId).toBeUndefined();
  });
});
