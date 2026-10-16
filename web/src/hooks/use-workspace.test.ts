import { beforeEach, describe, expect, it, vi } from "vitest";
import { useRef, useState } from "react";
import { createSession } from "../api/sessions";
import { streamChat } from "../api/chat-stream";
import { useWorkspace } from "./use-workspace";
import type { StreamState } from "../features/chat/reducer";

vi.mock("react", async (original) => ({
  ...await original<typeof import("react")>(),
  useState: vi.fn(),
  useRef: vi.fn(),
  useEffect: vi.fn(),
  useMemo: <T,>(factory: () => T) => factory(),
  useCallback: <T,>(callback: T) => callback,
}));
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn().mockResolvedValue(undefined), setQueryData: vi.fn() }),
  useQuery: ({ queryKey }: { queryKey: string[] }) => queryKey[0] === "projects"
    ? { isSuccess: true, data: { projects: [{ project_id: "project", name: "demo" }] } }
    : { isSuccess: true, data: {} },
  useInfiniteQuery: () => ({}),
  useMutation: () => ({ mutateAsync: createSession }),
}));
vi.mock("../api/sessions", () => ({ createSession: vi.fn(), deleteSession: vi.fn(), listAllSessions: vi.fn() }));
vi.mock("../api/chat-stream", () => ({ streamChat: vi.fn(), resumeChat: vi.fn(), answerApproval: vi.fn() }));

type Selection = { projectID: string; sessionID?: string };

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

// Keep hook state/refs across explicit renders; only API/request coordination is exercised.
function workspaceFixture() {
  let selection: Selection = { projectID: "project" };
  let streams: Record<string, StreamState> = {};
  const controllers = { current: new Map<string, AbortController>() };
  const sending = { current: new Set<string>() };
  return () => {
    vi.mocked(useState).mockReturnValueOnce([selection, (action: unknown) => {
      selection = typeof action === "function" ? (action as (current: Selection) => Selection)(selection) : action as Selection;
    }]).mockReturnValueOnce([streams, (action: unknown) => {
      streams = (action as (current: typeof streams) => typeof streams)(streams);
    }]);
    vi.mocked(useRef).mockReturnValueOnce(controllers).mockReturnValueOnce(sending);
    return useWorkspace("user");
  };
}

beforeEach(() => { vi.resetAllMocks(); });

describe("workspace send coordination", () => {
  it("lets another draft send while a previously created session is still streaming", async () => {
    const first = deferred();
    vi.mocked(createSession)
      .mockResolvedValueOnce({ session_id: "first", project_id: "project" } as Awaited<ReturnType<typeof createSession>>)
      .mockResolvedValueOnce({ session_id: "second", project_id: "project" } as Awaited<ReturnType<typeof createSession>>);
    vi.mocked(streamChat).mockImplementationOnce(() => first.promise).mockResolvedValueOnce(undefined);
    const render = workspaceFixture();
    const firstSend = render().send("first task");
    await vi.waitFor(() => expect(streamChat).toHaveBeenCalledTimes(1));
    expect(render().stream.running).toBe(true);
    render().startSession("project");
    expect(render().stream.running).toBe(false);
    await render().send("second task");
    expect(createSession).toHaveBeenCalledTimes(2);
    expect(vi.mocked(streamChat).mock.calls.map(([id, task]) => [id, task])).toEqual([["first", "first task"], ["second", "second task"]]);
    // The first session is still live and can be selected/canceled independently.
    expect(render().runningSessionIDs).toContain("first");
    first.resolve();
    await firstSend;
  });

  it("keeps a newer draft creation reserved when the old session finishes", async () => {
    const first = deferred(), creation = deferred();
    vi.mocked(createSession)
      .mockResolvedValueOnce({ session_id: "first", project_id: "project" } as Awaited<ReturnType<typeof createSession>>)
      .mockImplementationOnce(async () => {
        await creation.promise;
        return { session_id: "second", project_id: "project" } as Awaited<ReturnType<typeof createSession>>;
      });
    vi.mocked(streamChat).mockImplementationOnce(() => first.promise).mockResolvedValueOnce(undefined);
    const render = workspaceFixture();
    const firstSend = render().send("first task");
    await vi.waitFor(() => expect(streamChat).toHaveBeenCalledTimes(1));
    render().startSession("project");
    const draft = render();
    const secondSend = draft.send("second task");
    expect(createSession).toHaveBeenCalledTimes(2);
    first.resolve();
    await firstSend;
    await draft.send("duplicate task");
    expect(createSession).toHaveBeenCalledTimes(2);
    creation.resolve();
    await secondSend;
    expect(streamChat).toHaveBeenCalledTimes(2);
  });
});
