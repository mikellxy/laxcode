import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession, listSessions } from "../api/sessions";
import { listHistory } from "../api/history";
import { answerApproval, resumeChat, streamChat } from "../api/chat-stream";
import { getSessionContext } from "../api/context";
import { ApiError } from "../api/client";
import { streamReducer, type StreamState } from "../features/chat/reducer";
import type { ContextData, StreamEvent } from "../types/api";

const emptyStream = (): StreamState => ({ messages: [], running: false });

export function useWorkspace(userID: string) {
  const client = useQueryClient();
  const [selectedID, setSelectedID] = useState<string>();
  const [streams, setStreams] = useState<Record<string, StreamState>>({});
  const controllers = useRef(new Map<string, AbortController>());
  const bootstrapped = useRef(false);
  const sessions = useInfiniteQuery({
    queryKey: ["sessions", userID], queryFn: ({ pageParam }) => listSessions(userID, pageParam), initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.has_more ? page.next_before_session_id : undefined,
  });
  const allSessions = useMemo(() => sessions.data?.pages.flatMap((page) => page.sessions) ?? [], [sessions.data]);
  const create = useMutation({ mutationFn: () => createSession(userID), onSuccess: (created) => { client.setQueryData(["sessions", userID], (old: typeof sessions.data) => old ? { ...old, pages: [{ ...old.pages[0], sessions: [created, ...old.pages[0].sessions.filter((s) => s.session_id !== created.session_id)] }, ...old.pages.slice(1)] } : old); setSelectedID(created.session_id); } });

  useEffect(() => {
    if (!sessions.isSuccess || selectedID || bootstrapped.current) return;
    bootstrapped.current = true;
    if (allSessions[0]) setSelectedID(allSessions[0].session_id); else create.mutate();
  }, [sessions.isSuccess, selectedID, allSessions, create]);

  const history = useInfiniteQuery({
    queryKey: ["history", selectedID], enabled: Boolean(selectedID), queryFn: ({ pageParam }) => listHistory(selectedID!, pageParam), initialPageParam: undefined as number | undefined,
    getNextPageParam: (page) => page.has_more ? page.next_before_seq : undefined,
  });
  const historyMessages = history.data?.pages.slice().reverse().flatMap((page) => page.messages) ?? [];
  const contextUsage = useQuery({
    queryKey: ["context", selectedID], enabled: Boolean(selectedID), queryFn: () => getSessionContext(selectedID!),
  });

  const updateStream = useCallback((sessionID: string, action: Parameters<typeof streamReducer>[1]) => {
    setStreams((current) => ({ ...current, [sessionID]: streamReducer(current[sessionID] ?? emptyStream(), action) }));
  }, []);

  const reconcile = useCallback(async (sessionID: string, preserveError: boolean) => {
    await client.invalidateQueries({ queryKey: ["history", sessionID] });
    if (preserveError) await client.invalidateQueries({ queryKey: ["context", sessionID] });
    updateStream(sessionID, { type: "reconciled", preserveError });
    await client.invalidateQueries({ queryKey: ["sessions", userID] });
  }, [client, updateStream, userID]);

  const execute = useCallback(async (sessionID: string, mode: "send" | "resume", task?: string) => {
    const controller = new AbortController(); controllers.current.set(sessionID, controller);
    updateStream(sessionID, mode === "send" ? { type: "send", content: task! } : { type: "retry" });
    let failed = false;
    try {
      let terminal = false;
      const onEvent = (event: StreamEvent) => {
        updateStream(sessionID, { type: "event", event });
        if (event.type === "done") client.setQueryData<ContextData>(["context", sessionID], {
          window_token: event.data.window_token, context_window: event.data.context_window,
        });
        if (event.type === "done" || event.type === "error") terminal = true;
        if (event.type === "error") failed = true;
      };
      if (mode === "send") await streamChat(sessionID, task!, controller.signal, onEvent);
      else await resumeChat(sessionID, controller.signal, onEvent);
      if (!terminal) {
        failed = true;
        updateStream(sessionID, { type: "failed", message: "连接意外中断", code: "CONNECTION_INTERRUPTED", retryAction: "resume" });
      }
    } catch (error) {
      failed = true;
      const message = error instanceof DOMException && error.name === "AbortError" ? "已取消本轮对话" : error instanceof Error ? error.message : "发送失败";
      const apiError = error instanceof ApiError ? error : undefined;
      updateStream(sessionID, { type: "failed", message, code: apiError?.code, retryAction: apiError?.retryAction ?? (mode === "resume" ? "resume" : undefined) });
    } finally {
      controllers.current.delete(sessionID);
      await reconcile(sessionID, failed);
    }
  }, [updateStream, reconcile, client]);

  const approve = useCallback(async (approved: boolean) => {
    if (!selectedID) return;
    const approval = streams[selectedID]?.approval;
    if (!approval) return;
    await answerApproval(approval, approved);
    updateStream(selectedID, { type: "approval_resolved", approvalID: approval.approval_id });
  }, [selectedID, streams, updateStream]);

  const send = useCallback(async (task: string) => {
    if (!selectedID || streams[selectedID]?.running) return;
    await execute(selectedID, "send", task);
  }, [selectedID, streams, execute]);

  const retry = useCallback(async () => {
    if (!selectedID) return;
    const state = streams[selectedID];
    if (!state?.retryAction || state.running) return;
    if (state.retryAction === "resume") await execute(selectedID, "resume");
    else if (state.task) await execute(selectedID, "send", state.task);
  }, [selectedID, streams, execute]);

  const cancel = useCallback(() => { if (selectedID) controllers.current.get(selectedID)?.abort(); }, [selectedID]);
  useEffect(() => () => { controllers.current.forEach((controller) => controller.abort()); }, []);

  return { sessions, allSessions, create, selectedID, select: setSelectedID, history, historyMessages, contextUsage, stream: selectedID ? streams[selectedID] ?? emptyStream() : emptyStream(), send, retry, cancel, approve };
}
