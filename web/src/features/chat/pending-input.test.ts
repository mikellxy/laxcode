import { describe, expect, it } from "vitest";
import { canRetryPending, pendingCommitted } from "./pending-input";
import type { HistoryPageDTO } from "../../types/api";

const input = { text: "hello", created_at: "2026-10-27T12:00:00Z", base_message_seq: 11 };
const page: HistoryPageDTO = { task_status: "fail", can_resume: true, has_more: false, messages: [{ seq: 11, role: "tool", created_at: "" }] };
describe("pending input recovery", () => {
  it("offers retry after the last tool message even when the previous task says success", () => {
    expect(canRetryPending(input, page)).toBe(true);
    expect(canRetryPending(input, { ...page, task_status: "success" })).toBe(true);
    expect(canRetryPending(input, { ...page, task_status: "running" })).toBe(false);
  });
  it("requires a new matching persisted user message to confirm submission", () => {
    expect(pendingCommitted(input, { ...page, messages: [{ seq: 12, role: "tool", content: "hello", created_at: "" }] })).toBe(false);
    expect(pendingCommitted(input, { ...page, messages: [{ seq: 12, role: "user", content: "hello", created_at: "" }] })).toBe(true);
  });
});
