import type { HistoryPageDTO } from "../../types/api";

export type PendingInput = { text: string; created_at: string; base_message_seq: number; task_id?: string };
const key = (user: string, session: string) => `laxcode:pending:${user}:${session}`;
export function readPending(user: string, session: string): PendingInput | undefined {
  try {
    const value = JSON.parse(localStorage.getItem(key(user, session)) ?? "null");
    if (value && typeof value.text === "string" && typeof value.created_at === "string" && Number.isFinite(Date.parse(value.created_at)) && Number.isSafeInteger(value.base_message_seq)) return value;
  } catch { /* Storage may be unavailable in private browsing. */ }
}
export function writePending(user: string, session: string, input?: PendingInput) {
  try {
    if (input) localStorage.setItem(key(user, session), JSON.stringify(input));
    else localStorage.removeItem(key(user, session));
  } catch { /* The current page can still send; reload recovery needs storage. */ }
}
export function pendingCommitted(input: PendingInput, page: HistoryPageDTO) {
  return page.messages.some((message) => message.seq > input.base_message_seq && message.role === "user" && message.content === input.text);
}
export function canRetryPending(input: PendingInput | undefined, page: HistoryPageDTO | undefined) {
  return Boolean(input && page && page.task_status !== "waiting" && page.task_status !== "running" && (page.messages.at(-1)?.seq ?? 0) <= input.base_message_seq);
}
