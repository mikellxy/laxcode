import { beforeEach, describe, expect, it, vi } from "vitest";
import { useRef, useState } from "react";
import { createSession } from "../api/sessions";
import { cancelTask, resumeChat, streamChat, subscribeTask } from "../api/chat-stream";
import { listHistory } from "../api/history";
import { useWorkspace } from "./use-workspace";
import { readPending, writePending } from "../features/chat/pending-input";
import type { StreamState } from "../features/chat/reducer";
import type { HistoryPageDTO, SessionDTO } from "../types/api";

const state = vi.hoisted(() => ({
  page: { messages: [], has_more: false, can_resume: false } as HistoryPageDTO,
  effects: [] as (() => unknown)[],
  invalidate: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("react", async (original) => ({
  ...await original<typeof import("react")>(),
  useState: vi.fn(), useRef: vi.fn(),
  useEffect: (effect: () => unknown) => state.effects.push(effect),
  useMemo: <T,>(factory: () => T) => factory(),
  useCallback: <T,>(callback: T) => callback,
}));
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({
    invalidateQueries: state.invalidate,
    getQueryData: () => ({ pages: [state.page] }),
    setQueryData: (key: string[], value: { pages: HistoryPageDTO[] }) => { if (key[0] === "history") state.page = value.pages[0]; },
  }),
  useQuery: ({ queryKey }: { queryKey: string[] }) => queryKey[0] === "projects"
    ? { isSuccess: true, data: { projects: [{ project_id: "project", name: "demo" }] } }
    : { isSuccess: true, data: {} },
  useInfiniteQuery: () => ({ isSuccess: true, data: { pages: [state.page] } }),
  useMutation: () => ({ mutateAsync: createSession }),
}));
vi.mock("../api/sessions", () => ({ createSession: vi.fn(), deleteSession: vi.fn(), renameSession: vi.fn(), listAllSessions: vi.fn() }));
vi.mock("../api/chat-stream", () => ({ streamChat: vi.fn(), resumeChat: vi.fn(), subscribeTask: vi.fn(), cancelTask: vi.fn() }));
vi.mock("../api/history", () => ({ listHistory: vi.fn() }));

function fixture(draft = false) {
  let selection = { projectID: "project", sessionID: draft ? undefined : "s1" };
  let streams: Record<string, StreamState> = {};
  const controllers = { current: new Map<string, AbortController>() };
  const sending = { current: new Set<string>() };
  const stopRequests = { current: new Set<string>() };
  const creatingSession = { current: false };
  return () => {
    state.effects = [];
    vi.mocked(useState).mockReturnValueOnce([selection, (action: unknown) => { selection = typeof action === "function" ? action(selection) : action as typeof selection; }])
      .mockReturnValueOnce([streams, (action: unknown) => { streams = (action as (current: typeof streams) => typeof streams)(streams); }]);
    vi.mocked(useRef).mockReturnValueOnce(controllers).mockReturnValueOnce(sending).mockReturnValueOnce(stopRequests).mockReturnValueOnce(creatingSession);
    return useWorkspace("user");
  };
}
const finished: HistoryPageDTO = { task_id: "new", task_status: "success", messages: [], has_more: false, can_resume: false };
const active: HistoryPageDTO = { ...finished, task_status: "running" };
beforeEach(() => {
  vi.clearAllMocks(); state.page = { messages: [], has_more: false, can_resume: false };
  const store = new Map<string, string>();
  vi.stubGlobal("localStorage", { getItem: (key: string) => store.get(key) ?? null, setItem: (key: string, value: string) => store.set(key, value), removeItem: (key: string) => store.delete(key) });
  vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() });
  vi.mocked(listHistory).mockResolvedValue(finished);
  vi.mocked(streamChat).mockResolvedValue({ task_id: "new", task_status: "waiting" });
  vi.mocked(resumeChat).mockResolvedValue({ task_id: "new", task_status: "waiting" });
});

describe("asynchronous chat recovery", () => {
  const emptySession = (id: string): SessionDTO => ({ session_id: id, project_id: "project", user_id: "user", title: "", mode: "code", work_dir: "/workplace/demo", created_at: "", updated_at: "" });
  it("creates and selects a fresh empty session on each explicit click", async () => {
    const render = fixture(true);
    vi.mocked(createSession).mockResolvedValueOnce(emptySession("first")).mockResolvedValueOnce(emptySession("second"));
    await render().startSession("project");
    expect(render().selectedID).toBe("first");
    await render().startSession("project");
    expect(render().selectedID).toBe("second");
    expect(createSession).toHaveBeenCalledTimes(2);
    expect(streamChat).not.toHaveBeenCalled();
  });
  it("blocks repeated clicks while creation is in flight and permits retry after failure", async () => {
    const render = fixture(true);
    let fail!: (error: Error) => void;
    vi.mocked(createSession).mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject; }));
    const first = render().startSession("project");
    await render().startSession("project");
    expect(createSession).toHaveBeenCalledTimes(1);
    fail(new Error("offline"));
    await expect(first).rejects.toThrow("offline");
    expect(render().selectedID).toBeUndefined();
    vi.mocked(createSession).mockResolvedValueOnce(emptySession("retried"));
    await render().startSession("project");
    expect(render().selectedID).toBe("retried");
  });
  it("creates a real session on the first draft message and uses it for later messages", async () => {
    const render = fixture(true);
    vi.mocked(createSession).mockResolvedValueOnce(emptySession("first"));
    await render().send("hello");
    expect(render().selectedID).toBe("first");
    await render().send("again");
    expect(createSession).toHaveBeenCalledTimes(1);
    expect(streamChat).toHaveBeenNthCalledWith(1, "first", "hello");
    expect(streamChat).toHaveBeenNthCalledWith(2, "first", "again");
  });
  it("keeps the draft and retry action when session creation fails", async () => {
    const render = fixture(true);
    vi.mocked(createSession).mockRejectedValue(new Error("network error"));
    await render().send("keep this draft");
    expect(render().stream.messages[0].content).toBe("keep this draft");
    expect(render().retryAction).toBe("resend");
    await render().retry();
    expect(createSession).toHaveBeenCalledTimes(2);
    expect(streamChat).not.toHaveBeenCalled();
  });
  it("submits once, refreshes history before subscribing and reconciles a terminal task", async () => {
    const render = fixture();
    vi.mocked(listHistory).mockResolvedValueOnce(state.page).mockResolvedValueOnce(active).mockResolvedValueOnce(finished);
    vi.mocked(subscribeTask).mockImplementation(async (_id, _signal, onEvent) => {
      expect(listHistory).toHaveBeenCalledTimes(2);
      onEvent({ type: "task_state", data: { task_id: "new", task_status: "success" } });
    });
    await render().send("hello");
    expect(streamChat).toHaveBeenCalledWith("s1", "hello");
    expect(subscribeTask).toHaveBeenCalledTimes(1);
    expect(render().stream.running).toBe(false);
  });
  it("uses the displayed failed task ID for resume, not a newer task seen during submission", async () => {
    state.page = { ...finished, task_id: "old", task_status: "fail", can_resume: true };
    vi.mocked(listHistory).mockResolvedValue(finished);
    const render = fixture();
    await render().retry();
    expect(resumeChat).toHaveBeenCalledWith("s1", "old");
    expect(streamChat).not.toHaveBeenCalled();
  });
  it("retains an uncommitted input with the last tool sequence and retries via chat", async () => {
    state.page = { ...finished, task_status: "fail", messages: [{ seq: 11, role: "tool", created_at: "" }] };
    writePending("user", "s1", { text: "retry me", created_at: "2026-10-27T12:00:00Z", base_message_seq: 11 });
    const render = fixture();
    expect(render().canResend).toBe(true);
    expect(new Date(render().stream.messages[0].createdAt).toISOString()).toBe("2026-10-27T12:00:00.000Z");
    await render().retry();
    expect(streamChat).toHaveBeenCalledWith("s1", "retry me");
    expect(resumeChat).not.toHaveBeenCalled();
  });
  it("clears pending input only after finding its persisted user message", async () => {
    const render = fixture();
    vi.mocked(listHistory).mockResolvedValueOnce(state.page).mockResolvedValueOnce({ ...finished, messages: [{ seq: 2, role: "user", content: "hello", created_at: "" }] });
    await render().send("hello");
    expect(readPending("user", "s1")).toBeUndefined();
  });
  it("restores a running task after refresh without resubmitting chat", async () => {
    state.page = active;
    vi.mocked(listHistory).mockResolvedValueOnce(active).mockResolvedValueOnce(finished);
    vi.mocked(subscribeTask).mockImplementation(async (_id, _signal, onEvent) => { onEvent({ type: "task_state", data: finished as { task_id: string; task_status: "success" } }); });
    const render = fixture(); render();
    state.effects[1]();
    await vi.waitFor(() => expect(state.page.task_status).toBe("success"));
    expect(subscribeTask).toHaveBeenCalledTimes(1);
    expect(streamChat).not.toHaveBeenCalled();
  });
  it("reconnects the SSE subscription after network failure without submitting again", async () => {
    vi.useFakeTimers();
    try {
      const render = fixture();
      vi.mocked(listHistory).mockResolvedValueOnce(state.page).mockResolvedValueOnce(active).mockResolvedValueOnce(active).mockResolvedValueOnce(finished);
      vi.mocked(subscribeTask).mockRejectedValueOnce(new Error("network error")).mockImplementationOnce(async (_id, _signal, onEvent) => { onEvent({ type: "task_state", data: { task_id: "new", task_status: "success" } }); });
      const sending = render().send("hello");
      await vi.waitFor(() => expect(subscribeTask).toHaveBeenCalledTimes(1));
      await vi.advanceTimersByTimeAsync(2000);
      await sending;
      expect(subscribeTask).toHaveBeenCalledTimes(2);
      expect(streamChat).toHaveBeenCalledTimes(1);
      expect(resumeChat).not.toHaveBeenCalled();
    } finally { vi.useRealTimers(); }
  });
  it("stops the backend task using its task ID", async () => {
    state.page = active;
    const render = fixture();
    await render().cancel();
    expect(cancelTask).toHaveBeenCalledWith("new");
  });
  it("remembers stop clicked before acceptance and cancels the newly accepted task", async () => {
    state.page = { ...finished, task_id: "old", task_status: "fail" };
    let accept!: (task: { task_id: string; task_status: "waiting" }) => void;
    vi.mocked(streamChat).mockReturnValue(new Promise((resolve) => { accept = resolve; }));
    const render = fixture();
    const sent = render().send("hello");
    await vi.waitFor(() => expect(streamChat).toHaveBeenCalledTimes(1));
    await render().cancel();
    expect(cancelTask).not.toHaveBeenCalled();
    accept({ task_id: "new", task_status: "waiting" });
    await sent;
    expect(cancelTask).toHaveBeenCalledWith("new");
  });
  it("does not offer resume for a failed but closed loop", () => {
    state.page = { ...finished, task_status: "fail", task_error_message: "file accounting failed" };
    const workspace = fixture()();
    expect(workspace.retryAction).toBeUndefined();
    expect(workspace.stream.error).toBe("file accounting failed");
  });
});
