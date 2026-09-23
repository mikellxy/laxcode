import { describe, expect, it, vi } from "vitest";
import { streamReducer, type StreamState } from "./reducer";

vi.stubGlobal("crypto", { randomUUID: vi.fn(() => `id-${Math.random()}`) });
describe("streamReducer", () => {
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
  it("keeps the stream running while approval is pending", () => {
    const approval = { approval_id: "a", session_id: "s", kind: "bash_command" as const, content: "approve?" };
    let state: StreamState = { messages: [], running: true };
    state = streamReducer(state, { type: "event", event: { type: "approval_required", data: approval } });
    expect(state).toMatchObject({ running: true, approval });
    state = streamReducer(state, { type: "approval_resolved", approvalID: "a" });
    expect(state.approval).toBeUndefined();
    expect(state.running).toBe(true);
  });
});
