import type { RetryAction, StreamEvent, HistoryPageDTO } from "../../types/api";
import type { ChatMessage } from "../../types/chat";
import { randomUUID } from "../../utils/uuid";

export type StreamState = { messages: ChatMessage[]; running: boolean; taskID?: string; lastMessageSeq?: number; lastEventID?: number; waitingFor?: "sandbox" | "reply"; error?: string; errorCode?: string; retryAction?: RetryAction; task?: string };
export type StreamAction = { type: "accepted"; taskID: string } | { type: "restore"; page: HistoryPageDTO } | { type: "reconnecting" } | { type: "send"; content: string } | { type: "retry" } | { type: "event"; event: StreamEvent } | { type: "failed"; message: string; code?: string; retryAction?: RetryAction } | { type: "reconciled"; preserveError: boolean } | { type: "clear" };
const id = randomUUID;
const now = () => new Date().toISOString();

function appendAssistant(messages: ChatMessage[], field: "content" | "reasoningContent", delta: string, seq?: number): ChatMessage[] {
  const last = messages.at(-1);
  const reasoningStreaming = field === "reasoningContent";
  if (last?.role === "assistant" && last.status === "streaming" && last.seq === seq) return [...messages.slice(0, -1), { ...last, [field]: (last[field] ?? "") + delta, reasoningStreaming }];
  const assistant: ChatMessage = { id: seq ? `stream-${seq}` : id(), seq, role: "assistant", content: field === "content" ? delta : "", reasoningContent: field === "reasoningContent" ? delta : "", reasoningStreaming, createdAt: now(), status: "streaming" };
  return [...messages, assistant];
}

function completeLastAssistant(messages: ChatMessage[]): ChatMessage[] {
  const last = messages.at(-1);
  if (last?.role !== "assistant" || last.status !== "streaming") return messages;
  return [...messages.slice(0, -1), { ...last, reasoningStreaming: false, status: "completed" }];
}

export function streamReducer(state: StreamState, action: StreamAction): StreamState {
  if (action.type === "accepted") return { ...state, taskID: action.taskID };
  if (action.type === "restore") return { messages: [], running: action.page.task_status === "waiting" || action.page.task_status === "running", taskID: action.page.task_id ?? undefined, lastMessageSeq: action.page.messages.at(-1)?.seq ?? 0, lastEventID: 0, error: action.page.task_status === "fail" ? action.page.task_error_message : undefined };
  if (action.type === "reconnecting") return { ...state, running: true, waitingFor: undefined, error: "连接中断，正在重新连接…" };
  if (action.type === "clear") return { messages: [], running: false };
  if (action.type === "send") return { messages: [{ id: id(), role: "user", content: action.content, reasoningStreaming: false, createdAt: now(), status: "completed" }], running: true, task: action.content };
  if (action.type === "retry") return { ...state, messages: [], running: true, waitingFor: undefined, error: undefined, errorCode: undefined, retryAction: undefined };
  if (action.type === "reconciled") return action.preserveError ? { ...state, messages: [], running: false, waitingFor: undefined } : { messages: [], running: false };
  if (action.type === "failed") return { ...state, running: false, waitingFor: undefined, error: action.message, errorCode: action.code, retryAction: action.retryAction, messages: state.messages.map((m) => m.status === "streaming" ? { ...m, reasoningStreaming: false, status: "error" } : m) };
  const { event } = action;
  if (event.type === "task_state" || event.type === "refresh_required") return state;
  if ("event_id" in event.data && event.data.event_id !== undefined) {
    if (event.data.task_id !== state.taskID || event.data.event_id <= (state.lastEventID ?? 0)) return state;
    const seq = event.data.message_seq ?? 0;
    if (seq) {
      const last = state.messages.at(-1);
      if (seq < (state.lastMessageSeq ?? 0) || (seq === state.lastMessageSeq && last?.seq !== seq)) return state;
    }
    state = { ...state, lastEventID: event.data.event_id, lastMessageSeq: seq || state.lastMessageSeq };
  }
  if (event.type === "prepare") return { ...state, waitingFor: "sandbox" };
  if (event.type === "start") return { ...state, waitingFor: "reply" };
  if (event.type === "reasoning" || event.type === "message") {
    if (!event.data.delta) return state;
    return { ...state, waitingFor: event.data.delta.trim() ? undefined : state.waitingFor, messages: appendAssistant(state.messages, event.type === "reasoning" ? "reasoningContent" : "content", event.data.delta, event.data.message_seq) };
  }
  if (event.type === "tool_call") return { ...state, waitingFor: undefined, messages: [...completeLastAssistant(state.messages), { id: event.data.message_seq ? `stream-${event.data.message_seq}` : id(), seq: event.data.message_seq, role: "tool", content: "", reasoningStreaming: false, toolSummary: event.data.info, createdAt: now(), status: "completed" }] };
  if (event.type === "error") return streamReducer(state, { type: "failed", message: event.data.message, code: event.data.code, retryAction: event.data.retry_action });
  if (event.type === "done") {
    let found = false;
    const messages = state.messages.map((m) => { if (m.role === "assistant" && m.status === "streaming") { found = true; return { ...m, reasoningStreaming: false, status: "completed" as const }; } return m; });
    if (!found && event.data.result) messages.push({ id: id(), role: "assistant", content: event.data.result, reasoningStreaming: false, createdAt: now(), status: "completed" });
    return { ...state, messages, running: false, waitingFor: undefined };
  }
  return state;
}
