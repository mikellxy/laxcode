import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { streamReducer, type StreamState } from "./reducer";

beforeEach(() => vi.stubGlobal("crypto", { randomUUID: vi.fn(() => `id-${Math.random()}`) }));
afterEach(() => vi.unstubAllGlobals());
describe("streamReducer", () => {
  it("keeps preparation outside messages and waits for actual output after start", () => {
    let state = streamReducer({ messages: [], running: false }, { type: "send", content: "hello" });
    state = streamReducer(state, { type: "event", event: { type: "prepare", data: {} } });
    expect(state.waitingFor).toBe("sandbox");
    state = streamReducer(state, { type: "event", event: { type: "start", data: { session_id: "s" } } });
    expect(state.waitingFor).toBe("reply");
    expect(state.running).toBe(true);
    expect(state.messages.map((message) => message.role)).toEqual(["user"]);
  });
  it.each(["reasoning", "message"] as const)("clears waiting on the first visible %s delta", (type) => {
    let state: StreamState = { messages: [], running: true, waitingFor: "reply" };
    state = streamReducer(state, { type: "event", event: { type, data: { delta: "" } } });
    expect(state.waitingFor).toBe("reply");
    expect(state.messages).toHaveLength(0);
    state = streamReducer(state, { type: "event", event: { type, data: { delta: " \n" } } });
    expect(state.waitingFor).toBe("reply");
    state = streamReducer(state, { type: "event", event: { type, data: { delta: "hello" } } });
    expect(state.waitingFor).toBeUndefined();
    expect(state.running).toBe(true);
    expect(state.messages).toHaveLength(1);
  });
  it("clears waiting when tool execution begins", () => {
    const state = streamReducer({ messages: [], running: true, waitingFor: "reply" }, { type: "event", event: { type: "tool_call", data: { info: "read_file" } } });
    expect(state.waitingFor).toBeUndefined();
    expect(state.messages[0].role).toBe("tool");
  });
  it.each(["sandbox", "reply"] as const)("clears %s waiting after cancellation, errors, completion, retry, and reconciliation", (waitingFor) => {
    const state: StreamState = { messages: [], running: true, waitingFor };
    const done = { session_id: "s", result: "", token_used: { token_input: 0, token_output: 0 }, window_token: { token_input: 0, token_output: 0 }, context_window: 100 };
    for (const action of [
      { type: "failed", message: "已取消本轮对话" },
      { type: "event", event: { type: "error", data: { code: "SANDBOX_UNAVAILABLE", message: "failed", retry_action: "resend" } } },
      { type: "event", event: { type: "done", data: done } },
      { type: "retry" },
      { type: "reconciled", preserveError: true },
      { type: "clear" },
    ] satisfies Parameters<typeof streamReducer>[1][]) {
      expect(streamReducer(state, action).waitingFor).toBeUndefined();
    }
  });
  it("starts a new assistant after a tool call", () => {
    let state: StreamState = { messages: [], running: false };
    state = streamReducer(state, { type: "send", content: "run tests" });
    state = streamReducer(state, { type: "event", event: { type: "message", data: { delta: "Checking" } } });
    state = streamReducer(state, { type: "event", event: { type: "tool_call", data: { info: "bash(test)" } } });
    expect(state.messages[1]).toMatchObject({ role: "assistant", status: "completed", reasoningStreaming: false });
    expect(state.running).toBe(true);
    state = streamReducer(state, { type: "event", event: { type: "message", data: { delta: "Passed" } } });
    expect(state.messages.map((message) => message.role)).toEqual(["user", "assistant", "tool", "assistant"]);
    expect(state.messages.at(-1)?.content).toBe("Passed");
  });
  it("ends reasoning state when answer content starts", () => {
    let state: StreamState = { messages: [], running: true };
    state = streamReducer(state, { type: "event", event: { type: "reasoning", data: { delta: "Checking" } } });
    expect(state.messages[0]).toMatchObject({ status: "streaming", reasoningStreaming: true });
    state = streamReducer(state, { type: "event", event: { type: "message", data: { delta: "Done" } } });
    expect(state.messages[0]).toMatchObject({ status: "streaming", reasoningStreaming: false });
  });
  it("marks streaming messages complete on done without duplicating result", () => {
    const state: StreamState = { running: true, messages: [{ id: "a", role: "assistant", content: "done", reasoningStreaming: false, createdAt: "now", status: "streaming" }] };
    const next = streamReducer(state, { type: "event", event: { type: "done", data: { session_id: "s", result: "done", token_used: { token_input: 1, token_output: 1 }, window_token: { token_input: 1, token_output: 1 }, context_window: 100 } } });
    expect(next.messages).toHaveLength(1); expect(next.messages[0].status).toBe("completed"); expect(next.running).toBe(false);
  });
  it("keeps retry metadata after history reconciliation", () => {
    let state: StreamState = { messages: [], running: true, task: "original" };
    state = streamReducer(state, { type: "event", event: { type: "error", data: { code: "CHAT_FAILED", message: "boom", retry_action: "resume" } } });
    state = streamReducer(state, { type: "reconciled", preserveError: true });
    expect(state).toMatchObject({ running: false, error: "boom", retryAction: "resume", task: "original", messages: [] });
  });
  it("creates chat messages on HTTP without randomUUID", () => {
    vi.stubGlobal("crypto", { getRandomValues: (bytes: Uint8Array) => bytes.fill(0) });
    let state = streamReducer({ messages: [], running: false }, { type: "send", content: "hello" });
    state = streamReducer(state, { type: "event", event: { type: "message", data: { delta: "reply" } } });
    expect(state.messages.map((message) => message.role)).toEqual(["user", "assistant"]);
    expect(state.messages[0].id).toBe("00000000-0000-4000-8000-000000000000");
  });
});
