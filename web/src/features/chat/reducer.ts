import type { RetryAction, StreamEvent } from "../../types/api";
import type { ChatMessage } from "../../types/chat";
import { randomUUID } from "../../utils/uuid";

export type StreamState = { messages: ChatMessage[]; running: boolean; waitingFor?: "sandbox" | "reply"; error?: string; errorCode?: string; retryAction?: RetryAction; task?: string };
export type StreamAction = { type: "send"; content: string } | { type: "retry" } | { type: "event"; event: StreamEvent } | { type: "failed"; message: string; code?: string; retryAction?: RetryAction } | { type: "reconciled"; preserveError: boolean } | { type: "clear" };
const id = randomUUID;
const now = () => new Date().toISOString();

function appendAssistant(messages: ChatMessage[], field: "content" | "reasoningContent", delta: string): ChatMessage[] {
  const last = messages.at(-1);
  const reasoningStreaming = field === "reasoningContent";
  if (last?.role === "assistant" && last.status === "streaming") return [...messages.slice(0, -1), { ...last, [field]: (last[field] ?? "") + delta, reasoningStreaming }];
  const assistant: ChatMessage = { id: id(), role: "assistant", content: field === "content" ? delta : "", reasoningContent: field === "reasoningContent" ? delta : "", reasoningStreaming, createdAt: now(), status: "streaming" };
  return [...messages, assistant];
}

function completeLastAssistant(messages: ChatMessage[]): ChatMessage[] {
  const last = messages.at(-1);
  if (last?.role !== "assistant" || last.status !== "streaming") return messages;
  return [...messages.slice(0, -1), { ...last, reasoningStreaming: false, status: "completed" }];
}

export function streamReducer(state: StreamState, action: StreamAction): StreamState {
  if (action.type === "clear") return { messages: [], running: false };
  if (action.type === "send") return { messages: [{ id: id(), role: "user", content: action.content, reasoningStreaming: false, createdAt: now(), status: "completed" }], running: true, task: action.content };
  if (action.type === "retry") return { ...state, messages: [], running: true, waitingFor: undefined, error: undefined, errorCode: undefined, retryAction: undefined };
  if (action.type === "reconciled") return action.preserveError ? { ...state, messages: [], running: false, waitingFor: undefined } : { messages: [], running: false };
  if (action.type === "failed") return { ...state, running: false, waitingFor: undefined, error: action.message, errorCode: action.code, retryAction: action.retryAction, messages: state.messages.map((m) => m.status === "streaming" ? { ...m, reasoningStreaming: false, status: "error" } : m) };
  const { event } = action;
  if (event.type === "prepare") return { ...state, waitingFor: "sandbox" };
  if (event.type === "start") return { ...state, waitingFor: "reply" };
  if (event.type === "reasoning" || event.type === "message") {
    if (!event.data.delta) return state;
    return { ...state, waitingFor: event.data.delta.trim() ? undefined : state.waitingFor, messages: appendAssistant(state.messages, event.type === "reasoning" ? "reasoningContent" : "content", event.data.delta) };
  }
  if (event.type === "tool_call") return { ...state, waitingFor: undefined, messages: [...completeLastAssistant(state.messages), { id: id(), role: "tool", content: "", reasoningStreaming: false, toolSummary: event.data.info, createdAt: now(), status: "completed" }] };
  if (event.type === "error") return streamReducer(state, { type: "failed", message: event.data.message, code: event.data.code, retryAction: event.data.retry_action });
  if (event.type === "done") {
    let found = false;
    const messages = state.messages.map((m) => { if (m.role === "assistant" && m.status === "streaming") { found = true; return { ...m, reasoningStreaming: false, status: "completed" as const }; } return m; });
    if (!found && event.data.result) messages.push({ id: id(), role: "assistant", content: event.data.result, reasoningStreaming: false, createdAt: now(), status: "completed" });
    return { ...state, messages, running: false, waitingFor: undefined };
  }
  return state;
}
