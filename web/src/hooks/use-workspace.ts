import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession, deleteSession, listAllSessions } from "../api/sessions";
import { createProject, listProjects } from "../api/projects";
import { listHistory } from "../api/history";
import { answerApproval, resumeChat, streamChat } from "../api/chat-stream";
import { getSessionContext } from "../api/context";
import { ApiError } from "../api/client";
import { streamReducer, type StreamState } from "../features/chat/reducer";
import type { ContextData, SessionDTO, StreamEvent } from "../types/api";

const emptyStream = (): StreamState => ({ messages: [], running: false });

export function useWorkspace(userID: string) {
  const client = useQueryClient();
  const [selection, setSelection] = useState<{ projectID: string; sessionID?: string }>();
  const selectedID = selection?.sessionID;
  const selectedProjectID = selection?.projectID;
  const streamKey = selectedID ?? (selectedProjectID ? `draft:${selectedProjectID}` : undefined);
  const [streams, setStreams] = useState<Record<string, StreamState>>({});
  const controllers = useRef(new Map<string, AbortController>());
  const sending = useRef(new Set<string>());

  const projects = useQuery({ queryKey: ["projects", userID], queryFn: () => listProjects(userID) });
  const projectIDs = useMemo(() => projects.data?.projects.map((project) => project.project_id) ?? [], [projects.data]);
  const sessions = useQuery({
    queryKey: ["project-sessions", userID, projectIDs],
    enabled: projects.isSuccess,
    queryFn: async () => {
      const entries = await Promise.all(projectIDs.map(async (projectID) =>
        [projectID, await listAllSessions(userID, projectID)] as const));
      return Object.fromEntries(entries) as Record<string, SessionDTO[]>;
    },
  });
  const sessionsByProject = useMemo(() => sessions.data ?? {}, [sessions.data]);
  const allSessions = useMemo(
    () => projectIDs.flatMap((projectID) => sessionsByProject[projectID] ?? []),
    [projectIDs, sessionsByProject],
  );
  const selectedProject = projects.data?.projects.find((project) => project.project_id === selectedProjectID);

  const createProjectMutation = useMutation({
    mutationFn: ({ name, workDir }: { name: string; workDir?: string }) => createProject(userID, name, workDir),
    onSuccess: async (created) => {
      client.setQueryData(["projects", userID], (old: Awaited<ReturnType<typeof listProjects>> | undefined) =>
        ({ projects: [created, ...(old?.projects ?? []).filter((project) => project.project_id !== created.project_id)] }));
      setSelection({ projectID: created.project_id });
      await client.invalidateQueries({ queryKey: ["projects", userID] });
    },
  });
  const createSessionMutation = useMutation({
    mutationFn: (projectID: string) => createSession(userID, projectID),
    onSuccess: (created) => {
      client.setQueriesData<Record<string, SessionDTO[]>>(
        { queryKey: ["project-sessions", userID] },
        (old) => ({ ...old, [created.project_id]: [created, ...(old?.[created.project_id] ?? []).filter((item) => item.session_id !== created.session_id)] }),
      );
      void client.invalidateQueries({ queryKey: ["project-sessions", userID] });
    },
  });
  const deleteSessionMutation = useMutation({
    mutationFn: (session: SessionDTO) => deleteSession(userID, session.session_id),
    onSuccess: async (_, removed) => {
      await client.cancelQueries({ queryKey: ["project-sessions", userID] });
      client.setQueriesData<Record<string, SessionDTO[]>>(
        { queryKey: ["project-sessions", userID] },
        (old) => old ? { ...old, [removed.project_id]: (old[removed.project_id] ?? []).filter((session) => session.session_id !== removed.session_id) } : old,
      );
      const remaining = client.getQueryData<Record<string, SessionDTO[]>>(["project-sessions", userID, projectIDs])?.[removed.project_id] ?? [];
      setSelection((current) => current?.sessionID === removed.session_id
        ? { projectID: removed.project_id, sessionID: remaining[0]?.session_id } : current);
      setStreams((current) => {
        const next = { ...current };
        delete next[removed.session_id];
        return next;
      });
      client.removeQueries({ queryKey: ["history", removed.session_id] });
      client.removeQueries({ queryKey: ["context", removed.session_id] });
      await client.invalidateQueries({ queryKey: ["project-sessions", userID] });
    },
  });

  useEffect(() => {
    if (!sessions.isSuccess || selection) return;
    const first = allSessions[0];
    const projectID = first?.project_id ?? projectIDs[0];
    if (projectID) setSelection({ projectID, sessionID: first?.session_id });
  }, [sessions.isSuccess, selection, allSessions, projectIDs]);

  const select = (sessionID: string) => {
    const session = allSessions.find((item) => item.session_id === sessionID);
    if (session) setSelection({ projectID: session.project_id, sessionID });
  };
  const selectProject = (projectID: string) => {
    if (projectID === selectedProjectID) return;
    setSelection({ projectID, sessionID: sessionsByProject[projectID]?.[0]?.session_id });
  };
  const history = useInfiniteQuery({
    queryKey: ["history", selectedID], enabled: Boolean(selectedID),
    queryFn: ({ pageParam }) => listHistory(selectedID!, pageParam),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (page) => page.has_more ? page.next_before_seq : undefined,
  });
  const historyMessages = history.data?.pages.slice().reverse().flatMap((page) => page.messages) ?? [];
  const contextUsage = useQuery({
    queryKey: ["context", selectedID], enabled: Boolean(selectedID), queryFn: () => getSessionContext(selectedID!),
  });

  const updateStream = useCallback((sessionID: string, action: Parameters<typeof streamReducer>[1]) => {
    setStreams((current) => ({ ...current, [sessionID]: streamReducer(current[sessionID] ?? emptyStream(), action) }));
  }, []);

  const startSession = (projectID: string) => {
    updateStream(`draft:${projectID}`, { type: "clear" });
    setSelection({ projectID });
  };

  const reconcile = useCallback(async (sessionID: string, preserveError: boolean) => {
    await client.invalidateQueries({ queryKey: ["history", sessionID] });
    if (preserveError) await client.invalidateQueries({ queryKey: ["context", sessionID] });
    updateStream(sessionID, { type: "reconciled", preserveError });
    await client.invalidateQueries({ queryKey: ["project-sessions", userID] });
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
        // tool_call 事件在每轮模型提交后发出，携带最新窗口占用；流式中途直写
        // context 缓存，让侧边栏不必等 done 才刷新。
        if (event.type === "tool_call" && event.data.window_token && event.data.context_window)
          client.setQueryData<ContextData>(["context", sessionID], {
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
    if (!selection || !streamKey || !task.trim() || streams[streamKey]?.running || sending.current.has(streamKey)) return;
    let sendingKey = streamKey;
    sending.current.add(sendingKey);
    try {
      let sessionID = selectedID;
      if (!sessionID) {
        const created = await createSessionMutation.mutateAsync(selection.projectID);
        sessionID = created.session_id;
        // A running session must not keep the project's next draft reserved.
        sending.current.delete(sendingKey);
        sendingKey = sessionID;
        sending.current.add(sendingKey);
        updateStream(streamKey, { type: "clear" });
        setSelection((current) => current === selection ? { projectID: created.project_id, sessionID } : current);
      }
      await execute(sessionID, "send", task);
    } catch (error) {
      updateStream(sendingKey, { type: "send", content: task });
      updateStream(sendingKey, { type: "failed", message: error instanceof Error ? error.message : "会话创建失败", retryAction: "resend" });
    } finally {
      sending.current.delete(sendingKey);
    }
  }, [selection, streamKey, selectedID, streams, execute, createSessionMutation, updateStream]);

  const retry = useCallback(async () => {
    if (!streamKey) return;
    const state = streams[streamKey];
    if (!state?.retryAction || state.running) return;
    if (state.retryAction === "resume" && selectedID) await execute(selectedID, "resume");
    else if (state.task) await send(state.task);
  }, [selectedID, streamKey, streams, execute, send]);

  const cancel = useCallback(() => { if (selectedID) controllers.current.get(selectedID)?.abort(); }, [selectedID]);
  useEffect(() => () => { controllers.current.forEach((controller) => controller.abort()); }, []);

  return {
    projects, sessions, sessionsByProject, allSessions,
    createProject: createProjectMutation, createSession: createSessionMutation, deleteSession: deleteSessionMutation,
    selectedID, selectedProjectID, selectedProject, select, selectProject, startSession, history, historyMessages, contextUsage,
    stream: streamKey ? streams[streamKey] ?? emptyStream() : emptyStream(),
    runningSessionIDs: Object.keys(streams).filter((id) => streams[id].running),
    send, retry, cancel, approve,
  };
}
