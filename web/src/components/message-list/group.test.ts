import { describe, expect, it } from "vitest";
import type { ChatMessage } from "../../types/chat";
import { groupMessages } from "./group";

const message = (id: string, role: ChatMessage["role"]): ChatMessage =>
  ({ id, role, content: "", reasoningStreaming: false, createdAt: "2024-01-01T00:00:00Z", status: "completed" });

describe("groupMessages", () => {
  it("groups consecutive tool messages into one block", () => {
    const blocks = groupMessages([
      message("u1", "user"), message("a1", "assistant"),
      message("t1", "tool"), message("t2", "tool"), message("t3", "tool"),
      message("a2", "assistant"),
    ]);
    expect(blocks).toEqual([
      { kind: "single", message: message("u1", "user") },
      { kind: "single", message: message("a1", "assistant") },
      { kind: "tools", messages: [message("t1", "tool"), message("t2", "tool"), message("t3", "tool")] },
      { kind: "single", message: message("a2", "assistant") },
    ]);
  });

  it("separates tool blocks split by an assistant message", () => {
    const blocks = groupMessages([message("t1", "tool"), message("a1", "assistant"), message("t2", "tool")]);
    expect(blocks.map((block) => block.kind)).toEqual(["tools", "single", "tools"]);
    if (blocks[0].kind !== "tools" || blocks[2].kind !== "tools") throw new Error("expected tool blocks");
    expect(blocks[0].messages.map((m) => m.id)).toEqual(["t1"]);
    expect(blocks[2].messages.map((m) => m.id)).toEqual(["t2"]);
  });

  it("wraps a lone tool message in a group", () => {
    expect(groupMessages([message("t1", "tool")])).toEqual([{ kind: "tools", messages: [message("t1", "tool")] }]);
  });

  it("keeps the order of plain conversations", () => {
    const blocks = groupMessages([message("u1", "user"), message("a1", "assistant"), message("u2", "user")]);
    expect(blocks.map((block) => block.kind)).toEqual(["single", "single", "single"]);
  });

  it("returns no blocks for an empty list", () => {
    expect(groupMessages([])).toEqual([]);
  });
});
