import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createSession, deleteSession, renameSession, listAllSessions } from "../api/sessions";
import { createProject, listProjects, listProjectsWithDefault } from "../api/projects";
import { listHistory } from "../api/history";
import { resumeChat, streamChat, subscribeTask, cancelTask } from "../api/chat-stream";
import { getSessionContext } from "../api/context";
import { ApiError } from "../api/client";
import { streamReducer, type StreamState } from "../features/chat/reducer";
import type { ContextData, SessionDTO, HistoryPageDTO } from "../types/api";

import { readPending, writePending, pendingCommitted, canRetryPending } from "../features/chat/pending-input";

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
  const stopRequests = useRef(new Set<string>());
  const creatingSession = useRef(false);

  const projects = useQuery({ queryKey: ["projects", userID], queryFn: () => listProjectsWithDefault(userID), retry: false });
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
    mutationFn: ({ name }: { name: string }) => createProject(userID, name),
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

  const renameSessionMutation = useMutation({
    mutationFn: ({ session, title }: { session: SessionDTO; title: string }) => renameSession(userID, session.session_id, title),
    onSuccess: async (updated) => {
      await client.cancelQueries({ queryKey: ["project-sessions", userID] });
      client.setQueriesData<Record<string, SessionDTO[]>>(
        { queryKey: ["project-sessions", userID] },
        (old) => old ? { ...old, [updated.project_id]: (old[updated.project_id] ?? []).map(item => item.session_id === updated.session_id ? updated : item) } : old,
      );
      await client.invalidateQueries({ queryKey: ["project-sessions", userID] });
    },
  });

  useEffect(() => {
    if (selection) {
      if (selection.sessionID) { try { localStorage.setItem(`laxcode:selected:${userID}`, selection.sessionID); } catch { /* Browser storage is optional. */ } }
      return;
    }
    if (!sessions.isSuccess) return;
    let saved: string | null = null;
    try { saved = localStorage.getItem(`laxcode:selected:${userID}`); } catch { /* Browser storage is optional. */ }
    const first = allSessions.find((item) => item.session_id === saved) ?? allSessions[0];
    const projectID = first?.project_id ?? projectIDs[0];
    if (projectID) setSelection({ projectID, sessionID: first?.session_id });
  }, [sessions.isSuccess, selection, allSessions, projectIDs, userID]);

  const select = (sessionID: string) => {
    const session = allSessions.find((item) => item.session_id === sessionID);
    if (session) {
      setSelection({ projectID: session.project_id, sessionID });
      try { localStorage.setItem(`laxcode:selected:${userID}`, sessionID); } catch { /* Browser storage is optional. */ }
    }
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
  const page = history.data?.pages[0];
  const storedInput = selectedID ? readPending(userID, selectedID) : undefined;
  const pending = storedInput && page && !pendingCommitted(storedInput, page) ? storedInput : undefined;
  const local = streamKey ? streams[streamKey] ?? emptyStream() : emptyStream();
  const active = page?.task_status === "waiting" || page?.task_status === "running";
  const stream: StreamState = { ...local, running: local.running || active, error: local.error ?? (page?.task_status === "fail" ? page.task_error_message : undefined), messages: [
    ...(pending && (historyMessages.at(-1)?.seq ?? 0) <= pending.base_message_seq ? [{ id: "pending-input", role: "user" as const, content: pending.text, reasoningStreaming: false, createdAt: pending.created_at, status: "completed" as const }] : []),
    ...local.messages.filter((message) => (message.role !== "user" || !selectedID) && (!message.seq || message.seq > (historyMessages.at(-1)?.seq ?? 0))),
  ] };
  const historyReady = history.isSuccess && !history.isFetching && !history.isRefetchError;
  const canResume = historyReady && page?.can_resume === true;
  const canResend = historyReady && !stream.running && canRetryPending(pending, page);
  const retryAction = !stream.running ? (!selectedID ? local.retryAction : !canResend && canResume ? "resume" as const : undefined) : undefined;
  const contextUsage = useQuery({
    queryKey: ["context", selectedID], enabled: Boolean(selectedID), queryFn: () => getSessionContext(selectedID!),
  });

  const updateStream = useCallback((sessionID: string, action: Parameters<typeof streamReducer>[1]) => {
    setStreams((current) => ({ ...current, [sessionID]: streamReducer(current[sessionID] ?? emptyStream(), action) }));
  }, []);

  const startSession = async (projectID: string) => {
    if (creatingSession.current) return;
    creatingSession.current = true;
    try {
      const created = await createSessionMutation.mutateAsync(projectID);
      updateStream(`draft:${projectID}`, { type: "clear" });
      setSelection({ projectID, sessionID: created.session_id });
    } finally { creatingSession.current = false; }
  };

  const follow = useCallback(async (sessionID: string) => {
    if (controllers.current.get(sessionID) && !controllers.current.get(sessionID)!.signal.aborted) return;
    const controller = new AbortController();
    controllers.current.set(sessionID, controller);
    const pause = () => new Promise<void>((resolve) => {
      const done = () => { clearTimeout(timer); controller.signal.removeEventListener("abort", done); resolve(); };
      const timer = setTimeout(done, 2000);
      controller.signal.addEventListener("abort", done, { once: true });
    });
    try {
      while (!controller.signal.aborted) {
        try {
          const latest = await listHistory(sessionID);
          if (controller.signal.aborted) return;
          const input = readPending(userID, sessionID);
          if (input && pendingCommitted(input, latest)) writePending(userID, sessionID);
          updateStream(sessionID, { type: "restore", page: latest });
          client.setQueryData(["history", sessionID], { pages: [latest], pageParams: [undefined] });
          if (!latest.task_id || (latest.task_status !== "waiting" && latest.task_status !== "running")) {
            void client.invalidateQueries({ queryKey: ["context", sessionID] });
            void client.invalidateQueries({ queryKey: ["project-sessions", userID] });
            return;
          }
          let terminal = false;
          let refreshRequired = false;
          await subscribeTask(latest.task_id, controller.signal, (event) => {
            if (controller.signal.aborted) return;
            if (event.type === "task_state" || event.type === "refresh_required") { terminal = true; refreshRequired = event.type === "refresh_required"; return; }
            updateStream(sessionID, { type: "event", event });
            if (event.type === "tool_call" && event.data.window_token && event.data.context_window) {
              client.setQueryData<ContextData>(["context", sessionID], { window_token: event.data.window_token, context_window: event.data.context_window });
            }
          });
          if ((!terminal || refreshRequired) && !controller.signal.aborted) { updateStream(sessionID, { type: "reconnecting" }); await pause(); }
        } catch {
          if (controller.signal.aborted) return;
          updateStream(sessionID, { type: "reconnecting" });
          await pause();
        }
      }
    } finally {
      if (controllers.current.get(sessionID) === controller) controllers.current.delete(sessionID);
    }
  }, [client, updateStream, userID]);

  useEffect(() => {
    if (selectedID && historyReady && active) void follow(selectedID);
  }, [selectedID, historyReady, active, follow]);
  useEffect(() => {
    const changed = () => setStreams((current) => ({ ...current }));
    window.addEventListener("storage", changed);
    return () => window.removeEventListener("storage", changed);
  }, []);

  const execute = useCallback(async (sessionID: string, mode: "send" | "resume", task?: string) => {
    if (controllers.current.has(sessionID)) return;
    const previous = client.getQueryData<{ pages: HistoryPageDTO[] }>(["history", sessionID])?.pages[0];
    const createdAt = new Date().toISOString();
    if (mode === "send") {
      writePending(userID, sessionID, { text: task!, created_at: createdAt, base_message_seq: previous?.messages.at(-1)?.seq ?? 0 });
      updateStream(sessionID, { type: "send", content: task! });
      updateStream(sessionID, { type: "event", event: { type: "prepare", data: {} } });
    } else updateStream(sessionID, { type: "retry" });
    try {
      const latest = await listHistory(sessionID);
      if (mode === "send") writePending(userID, sessionID, { text: task!, created_at: createdAt, base_message_seq: latest.messages.at(-1)?.seq ?? 0 });
      const accepted = mode === "send" ? await streamChat(sessionID, task!) : await resumeChat(sessionID, previous?.task_id);
      if (mode === "send") {
        const input = readPending(userID, sessionID);
        if (input) writePending(userID, sessionID, { ...input, task_id: accepted.task_id });
      }
      updateStream(sessionID, { type: "accepted", taskID: accepted.task_id });
      if (stopRequests.current.delete(sessionID)) await cancelTask(accepted.task_id);
      await follow(sessionID);
    } catch (error) {
      updateStream(sessionID, { type: "failed", message: error instanceof Error ? error.message : "提交失败，请重试" });
      if (error instanceof ApiError && error.refreshAfterMs) await new Promise((resolve) => setTimeout(resolve, error.refreshAfterMs));
      await client.invalidateQueries({ queryKey: ["history", sessionID] });
    } finally { stopRequests.current.delete(sessionID); }
  }, [client, userID, updateStream, follow]);

  const send = useCallback(async (task: string) => {
    if (!selection || !streamKey || !task.trim() || streams[streamKey]?.running || sending.current.has(streamKey) || creatingSession.current) return;
    let sendingKey = streamKey;
    sending.current.add(sendingKey);
    try {
      let sessionID = selectedID;
      if (!sessionID) {
        creatingSession.current = true;
        let created: SessionDTO;
        try { created = await createSessionMutation.mutateAsync(selection.projectID); }
        finally { creatingSession.current = false; }
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
    if (!streamKey || stream.running) return;
    if (canResend && pending) await send(pending.text);
    else if (retryAction === "resend" && stream.task) await send(stream.task);
    else if (retryAction === "resume" && selectedID) await execute(selectedID, "resume");
  }, [streamKey, stream.running, stream.task, canResend, pending, send, retryAction, selectedID, execute]);

  const cancel = useCallback(async () => {
    if (selectedID && sending.current.has(selectedID) && !local.taskID) { stopRequests.current.add(selectedID); return; }
    const taskID = local.taskID ?? page?.task_id;
    if (!selectedID || !taskID) return;
    try { await cancelTask(taskID); }
    catch (error) { updateStream(selectedID, { type: "reconnecting" }); if (error instanceof ApiError) void client.invalidateQueries({ queryKey: ["history", selectedID] }); }
  }, [selectedID, local.taskID, page?.task_id, client, updateStream]);
  useEffect(() => () => { controllers.current.forEach((controller) => controller.abort()); }, []);

  return {
    projects, sessions, sessionsByProject, allSessions,
    createProject: createProjectMutation, createSession: createSessionMutation, deleteSession: deleteSessionMutation, renameSession: renameSessionMutation,
    selectedID, selectedProjectID, selectedProject, select, selectProject, startSession, history, historyMessages, contextUsage,
    stream, retryAction, canResend,
    runningSessionIDs: Object.keys(streams).filter((id) => streams[id].running),
    send, retry, cancel,
  };
}
