import type { HistoryMessageDTO } from "./api";

export type ChatMessage = { id: string; seq?: number; role: "user" | "assistant" | "tool"; content: string; reasoningContent?: string; reasoningStreaming: boolean; toolSummary?: string; createdAt: string; status: "streaming" | "completed" | "error" };

export const historyToMessage = (message: HistoryMessageDTO): ChatMessage => ({ id: `history-${message.seq}`, seq: message.seq, role: message.role, content: message.content ?? "", reasoningContent: message.reasoning_content ?? "", reasoningStreaming: false, toolSummary: message.tool_summary ?? "", createdAt: message.created_at, status: "completed" });
