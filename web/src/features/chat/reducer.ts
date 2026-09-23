import type { ApprovalRequiredData, RetryAction, StreamEvent } from "../../types/api";
import type { ChatMessage } from "../../types/chat";

export type StreamState = { messages: ChatMessage[]; running: boolean; error?: string; errorCode?: string; retryAction?: RetryAction; task?: string; approval?: ApprovalRequiredData };
export type StreamAction = { type: "send"; content: string } | { type: "retry" } | { type: "event"; event: StreamEvent } | { type: "approval_resolved"; approvalID: string } | { type: "failed"; message: string; code?: string; retryAction?: RetryAction } | { type: "reconciled"; preserveError: boolean } | { type: "clear" };
const id = () => crypto.randomUUID();
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
  if (action.type === "retry") return { ...state, messages: [], running: true, error: undefined, errorCode: undefined, retryAction: undefined };
  if (action.type === "approval_resolved") return state.approval?.approval_id === action.approvalID ? { ...state, approval: undefined } : state;
  if (action.type === "reconciled") return action.preserveError ? { ...state, messages: [], running: false, approval: undefined } : { messages: [], running: false };
  if (action.type === "failed") return { ...state, running: false, approval: undefined, error: action.message, errorCode: action.code, retryAction: action.retryAction, messages: state.messages.map((m) => m.status === "streaming" ? { ...m, reasoningStreaming: false, status: "error" } : m) };
  const { event } = action;
  if (event.type === "reasoning") return { ...state, messages: appendAssistant(state.messages, "reasoningContent", event.data.delta) };
  if (event.type === "message") return { ...state, messages: appendAssistant(state.messages, "content", event.data.delta) };
  if (event.type === "tool_call") return { ...state, messages: [...completeLastAssistant(state.messages), { id: id(), role: "tool", content: "", reasoningStreaming: false, toolSummary: event.data.info, createdAt: now(), status: "completed" }] };
  if (event.type === "approval_required") return { ...state, approval: event.data };
  if (event.type === "error") return streamReducer(state, { type: "failed", message: event.data.message, code: event.data.code, retryAction: event.data.retry_action });
  if (event.type === "done") {
    let found = false;
    const messages = state.messages.map((m) => { if (m.role === "assistant" && m.status === "streaming") { found = true; return { ...m, reasoningStreaming: false, status: "completed" as const }; } return m; });
    if (!found && event.data.result) messages.push({ id: id(), role: "assistant", content: event.data.result, reasoningStreaming: false, createdAt: now(), status: "completed" });
    return { ...state, messages, running: false, approval: undefined };
  }
  return state;
}
