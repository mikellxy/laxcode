import { ApiError } from "./client";
import type { ErrorPayload, StreamEvent } from "../types/api";

const eventNames = new Set(["start", "reasoning", "message", "tool_call", "done", "error"]);

export function parseSSEFrame(frame: string): StreamEvent | null {
  let event = "message";
  const data: string[] = [];
  for (const line of frame.split(/\r?\n/)) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
  }
  if (!eventNames.has(event) || !data.length) return null;
  try { return { type: event, data: JSON.parse(data.join("\n")) } as StreamEvent; } catch { return null; }
}

async function consumeSSE(url: string, init: RequestInit, signal: AbortSignal, onEvent: (event: StreamEvent) => void) {
  const response = await fetch(url, { ...init, headers: { Accept: "text/event-stream", ...init.headers }, signal });
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as ErrorPayload | null;
    throw new ApiError(payload?.message || `对话请求失败 (${response.status})`, response.status, payload?.code, payload?.retry_action);
  }
  if (!response.headers.get("content-type")?.toLowerCase().includes("text/event-stream") || !response.body) throw new ApiError("服务端未返回 SSE 数据流", response.status);

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value, { stream: !done }).replace(/\r\n/g, "\n");
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const event = parseSSEFrame(buffer.slice(0, boundary));
      buffer = buffer.slice(boundary + 2);
      if (event) onEvent(event);
      boundary = buffer.indexOf("\n\n");
    }
    if (done) break;
  }
  if (buffer.trim()) { const event = parseSSEFrame(buffer); if (event) onEvent(event); }
}

export const streamChat = (sessionID: string, task: string, signal: AbortSignal, onEvent: (event: StreamEvent) => void) => consumeSSE("/chat", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: sessionID, task }) }, signal, onEvent);
export const resumeChat = (sessionID: string, signal: AbortSignal, onEvent: (event: StreamEvent) => void) => consumeSSE(`/api/sessions/${encodeURIComponent(sessionID)}/resume`, { method: "POST" }, signal, onEvent);
