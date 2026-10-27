import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Menu, PanelLeftClose, PanelLeftOpen, Sparkles } from "lucide-react";
import { getOrCreateGuestID } from "../features/identity/guest";
import { useWorkspace } from "../hooks/use-workspace";
import { historyToMessage } from "../types/chat";
import { SessionTabs } from "../components/session-tabs/SessionTabs";
import { workDirLabel } from "../components/session-tabs/label";
import { MessageList } from "../components/message-list/MessageList";
import { ConfirmDialog } from "../components/confirm-dialog/ConfirmDialog";
import { ModelPicker } from "../components/model-picker/ModelPicker";
import { ChatComposer } from "../components/chat-composer/ChatComposer";
import { ProjectOnboarding } from "../components/project-onboarding/ProjectOnboarding";
import { CreateProjectDialog } from "../components/project-onboarding/CreateProjectDialog";
import { EvaluationPanel } from "../components/evaluation-panel/EvaluationPanel";
import { listModels } from "../api/models";
import type { SessionDTO } from "../types/api";
import { getUserConfig, updateUserConfig, type UserConfig } from "../api/user-config";
import { ApiError } from "../api/client";
import { UserGuide } from "../components/user-guide/UserGuide";
import { CopyableID } from "../components/copyable-id/CopyableID";

export function App() {
  const userID = useMemo(getOrCreateGuestID, []);
  const client = useQueryClient();
  const [dismissed, setDismissed] = useState(false);
  const config = useQuery({ queryKey: ["user-config", userID], queryFn: getUserConfig, staleTime: Infinity });
  const saveConfig = useMutation({
    mutationFn: updateUserConfig,
    onSuccess: (_, update) => client.setQueryData<UserConfig>(["user-config", userID], (previous) => previous ? { ...previous, ...update } : previous),
  });
  if (config.isError) return <div className="fatal"><Sparkles /><h1>暂时无法连接 LaxCode</h1><p>无法读取用户配置，请重试。</p><button onClick={() => void config.refetch()}>重新连接</button></div>;
  if (!config.data) return <div className="fatal" role="status"><LoaderCircle className="spin" /><p>正在加载…</p></div>;
  const finishGuide = () => {
    setDismissed(true);
    saveConfig.mutate({ user_guide_seen_version: config.data.user_guide_version });
  };
  if (!dismissed && config.data.user_guide_seen_version !== config.data.user_guide_version) return <UserGuide onFinish={finishGuide} />;
  const versionChanged = saveConfig.error instanceof ApiError && saveConfig.error.status === 409;
  return <><WorkspaceApp userID={userID} />{saveConfig.isError && <div className="guide-save-notice" role="alert"><span>{versionChanged ? "引导页已更新，请刷新页面后查看。" : "引导阅读状态保存失败，下次打开可能再次显示。"}</span><button onClick={versionChanged ? () => location.reload() : () => { if (saveConfig.variables) saveConfig.mutate(saveConfig.variables); }}>{versionChanged ? "刷新页面" : "重试保存"}</button></div>}</>;
}

function WorkspaceApp({ userID }: { userID: string }) {
  const workspace = useWorkspace(userID); const [sidebarOpen, setSidebarOpen] = useState(false); const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [sessionToDelete, setSessionToDelete] = useState<{ session: SessionDTO; label: string }>();
  const [deleteError, setDeleteError] = useState("");
  const [creatingProject, setCreatingProject] = useState(false);
  const createProject = () => {
    if (!workspace.createProject.isPending && !workspace.createSession.isPending) setCreatingProject(true);
  };
  const deleteSession = async () => {
    if (!sessionToDelete || workspace.deleteSession.isPending) return;
    setDeleteError("");
    try {
      await workspace.deleteSession.mutateAsync(sessionToDelete.session);
      setSessionToDelete(undefined);
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : "会话删除失败");
    }
  };
  const messages = [...workspace.historyMessages.map(historyToMessage), ...workspace.stream.messages];
  // 模型目录与 ModelPicker 共用同一 Query 缓存：无活跃模型时锁定输入框，
  // 平台尚未配置模型时显示不可用提示。
  const models = useQuery({ queryKey: ["models"], queryFn: listModels });
  const modelLocked = models.isSuccess && !models.data.current_model;
  const selected = workspace.allSessions.find((session) => session.session_id === workspace.selectedID);
  const workDir = selected?.work_dir ?? workspace.selectedProject?.work_dir;
  const displayWorkDir = workDirLabel(workspace.selectedProject?.name);
  const projects = workspace.projects.data?.projects ?? [];
  const isFirstRun = workspace.projects.isSuccess && projects.length === 0;
  const connectionError = workspace.projects.error ?? workspace.sessions.error;
  if (workspace.projects.isError || workspace.sessions.isError) return <div className="fatal"><Sparkles /><h1>暂时无法连接 LaxCode</h1><p>{connectionError instanceof Error ? connectionError.message : "请确认 SSE 服务已启动。"}</p><button onClick={() => { void workspace.projects.refetch(); void workspace.sessions.refetch(); }}>重新连接</button></div>;
  return <div className={`app-shell ${sidebarCollapsed ? "sidebar-collapsed" : ""} ${sidebarOpen ? "mobile-sidebar-open" : ""}`}>
    <div className="mobile-backdrop" onClick={() => setSidebarOpen(false)} />
    <SessionTabs projects={projects} sessionsByProject={workspace.sessionsByProject} selectedID={workspace.selectedID} selectedProjectID={workspace.selectedProjectID} contextUsage={workspace.contextUsage.data} creatingProject={workspace.createProject.isPending} creatingSessionProjectID={workspace.createSession.isPending ? workspace.createSession.variables : undefined} deletingSessionID={workspace.deleteSession.isPending ? workspace.deleteSession.variables?.session_id : undefined} runningSessionIDs={workspace.runningSessionIDs} onSelect={(id) => { workspace.select(id); setSidebarOpen(false); }} onSelectProject={workspace.selectProject} onDeleteSession={(session, label) => { setDeleteError(""); setSessionToDelete({ session, label }); }} onCreateProject={() => void createProject()} onCreateSession={(projectID) => { workspace.startSession(projectID); setSidebarOpen(false); }} />
    <section className="workspace">
      <header className="topbar"><button className="mobile-menu" onClick={() => setSidebarOpen(true)} aria-label="打开侧边栏"><Menu size={20} /></button><button className="collapse-button" onClick={() => setSidebarCollapsed((value) => !value)} aria-label={sidebarCollapsed ? "打开侧边栏" : "关闭侧边栏"}>{sidebarCollapsed ? <PanelLeftOpen size={19} /> : <PanelLeftClose size={19} />}</button><div className="workspace-meta"><strong title={displayWorkDir}>{isFirstRun ? "欢迎使用 LaxCode" : displayWorkDir || "请选择项目"}</strong><small className="workspace-session">{selected && <CopyableID key={selected.session_id} label="session_id" value={selected.session_id} />}<span className="workspace-status">{isFirstRun ? "创建项目后即可开始" : workspace.createSession.isPending ? "正在创建会话…" : workspace.stream.running ? <><i className="working-dot" />正在执行</> : "准备就绪"}</span></small><small><CopyableID label="user_id" value={userID} /></small></div><EvaluationPanel key={selected?.session_id ?? "no-session"} sessionID={selected?.session_id} workDir={workDir} disabled={workspace.stream.running || modelLocked} /></header>
      {isFirstRun ? <ProjectOnboarding creating={workspace.createProject.isPending || workspace.createSession.isPending} onCreate={() => void createProject()} /> : <>
        <MessageList messages={messages} sessionID={workspace.selectedID} hasMore={Boolean(workspace.history.hasNextPage)} loading={workspace.history.isFetchingNextPage} initialLoading={workspace.history.isLoading} error={workspace.stream.error} retryAction={workspace.retryAction} retrying={workspace.stream.running} waitingFor={workspace.stream.waitingFor} onRetry={workspace.retry} onLoadMore={() => workspace.history.fetchNextPage()} />
        <ModelPicker running={workspace.stream.running} />
        <ChatComposer running={workspace.stream.running} disabled={!workspace.selectedProject || !workspace.sessions.isSuccess || workspace.history.isLoading || workspace.createSession.isPending || (workspace.deleteSession.isPending && workspace.deleteSession.variables?.session_id === workspace.selectedID)} locked={modelLocked} onSend={workspace.send} onCancel={workspace.cancel} />
      </>}
    </section>
    {creatingProject && <CreateProjectDialog pending={workspace.createProject.isPending} onCancel={() => setCreatingProject(false)} onCreate={async (name) => {
      await workspace.createProject.mutateAsync({ name });
      setCreatingProject(false);
      setSidebarOpen(false);
      void models.refetch();
    }} />}
    {sessionToDelete && <ConfirmDialog variant="danger" title="删除这个会话？" description="会话历史和会话文件将被永久删除，此操作无法撤销。" confirmLabel="删除会话" cancelLabel="取消" pending={workspace.deleteSession.isPending} error={deleteError} onConfirm={() => void deleteSession()} onCancel={() => setSessionToDelete(undefined)}>
      <div className="confirm-session"><strong>{sessionToDelete.label}</strong><span>{workDirLabel(projects.find((project) => project.project_id === sessionToDelete.session.project_id)?.name)}</span></div>
    </ConfirmDialog>}
  </div>;
}
