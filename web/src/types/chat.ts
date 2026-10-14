import type { ChatFileDTO, HistoryMessageDTO } from "./api";

export type ChatMessage = { id: string; seq?: number; role: "user" | "assistant" | "tool"; content: string; reasoningContent?: string; reasoningStreaming: boolean; toolSummary?: string; chatId?: string; upsertFiles?: ChatFileDTO[]; createdAt: string; status: "streaming" | "completed" | "error" };

export const historyToMessage = (message: HistoryMessageDTO): ChatMessage => ({ id: `history-${message.seq}`, seq: message.seq, role: message.role, content: message.content ?? "", reasoningContent: message.reasoning_content ?? "", reasoningStreaming: false, toolSummary: message.tool_summary ?? "", chatId: message.chat_id || undefined, upsertFiles: message.upsert_files, createdAt: message.created_at, status: "completed" });
