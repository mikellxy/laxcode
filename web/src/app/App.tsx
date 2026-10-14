import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Menu, PanelLeftClose, PanelLeftOpen, Sparkles } from "lucide-react";
import { getOrCreateGuestID } from "../features/identity/guest";
import { useWorkspace } from "../hooks/use-workspace";
import { historyToMessage } from "../types/chat";
import { SessionTabs } from "../components/session-tabs/SessionTabs";
import { MessageList } from "../components/message-list/MessageList";
import { CommandBlock } from "../components/approval/CommandBlock";
import { ConfirmDialog } from "../components/confirm-dialog/ConfirmDialog";
import { ModelPicker } from "../components/model-picker/ModelPicker";
import { ChatComposer } from "../components/chat-composer/ChatComposer";
import { ProjectOnboarding } from "../components/project-onboarding/ProjectOnboarding";
import { CreateProjectDialog } from "../components/project-onboarding/CreateProjectDialog";
import { EvaluationPanel } from "../components/evaluation-panel/EvaluationPanel";
import { pickDirectory } from "../api/directory-picker";
import { requestJSON } from "../api/client";
import { listModels } from "../api/models";
import type { SessionDTO } from "../types/api";

export function App() {
  const mode = useQuery({ queryKey: ["server-config"], queryFn: () => requestJSON<{ cloud: boolean }>("/api/config") });
  const cloud = mode.data?.cloud === true;
  const userID = useMemo(getOrCreateGuestID, []); const workspace = useWorkspace(userID); const [sidebarOpen, setSidebarOpen] = useState(false); const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [approvalSubmitting, setApprovalSubmitting] = useState(false); const [approvalError, setApprovalError] = useState("");
  const [sessionToDelete, setSessionToDelete] = useState<{ session: SessionDTO; label: string }>();
  const [deleteError, setDeleteError] = useState("");
  const [pickingProject, setPickingProject] = useState(false);
  const [creatingCloudProject, setCreatingCloudProject] = useState(false);
  useEffect(() => { setApprovalError(""); setApprovalSubmitting(false); }, [workspace.stream.approval?.approval_id]);
  const answer = async (approved: boolean) => {
    if (approvalSubmitting) return;
    setApprovalSubmitting(true); setApprovalError("");
    try { await workspace.approve(approved); } catch (error) { setApprovalError(error instanceof Error ? error.message : "确认提交失败"); }
    finally { setApprovalSubmitting(false); }
  };
  const createProject = async () => {
    if (pickingProject || workspace.createProject.isPending || workspace.createSession.isPending) return;
    if (cloud) { setCreatingCloudProject(true); return; }
    setPickingProject(true);
    try {
      const { path: workDir } = await pickDirectory();
      if (!workDir) return;
      const suggestedName = workDir.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || "新项目";
      const name = window.prompt("请输入项目名称", suggestedName)?.trim();
      if (!name) return;
      await workspace.createProject.mutateAsync({ name, workDir });
      setSidebarOpen(false);
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      window.alert(error instanceof Error ? error.message : "项目创建失败");
    } finally {
      setPickingProject(false);
    }
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
  // 点击锁定区让齿轮按钮跳动引导配置。
  const models = useQuery({ queryKey: ["models"], queryFn: listModels });
  const modelLocked = models.isSuccess && !models.data.current_model;
  const [gearBounce, setGearBounce] = useState(0);
  const selected = workspace.allSessions.find((session) => session.session_id === workspace.selectedID);
  const workDir = selected?.work_dir ?? workspace.selectedProject?.work_dir;
  const projects = workspace.projects.data?.projects ?? [];
  const isFirstRun = workspace.projects.isSuccess && projects.length === 0;
  // 主工作区首次确认尚未配置模型后，稍作停顿再提示一次，避免页面刚加载时
  // 与其他入场动画争抢注意力；项目引导页不展示模型控件，故不提前触发。
  useEffect(() => {
    if (!modelLocked || !workspace.projects.isSuccess || isFirstRun) return;
    const timer = window.setTimeout(() => setGearBounce((value) => value + 1), 500);
    return () => window.clearTimeout(timer);
  }, [modelLocked, workspace.projects.isSuccess, isFirstRun]);
  const connectionError = mode.error ?? workspace.projects.error ?? workspace.sessions.error;
  if (mode.isPending) return <div className="fatal"><p>正在连接 LaxCode…</p></div>;
  if (mode.isError || workspace.projects.isError || workspace.sessions.isError) return <div className="fatal"><Sparkles /><h1>暂时无法连接 LaxCode</h1><p>{connectionError instanceof Error ? connectionError.message : "请确认 SSE 服务已启动。"}</p><button onClick={() => { void mode.refetch(); void workspace.projects.refetch(); void workspace.sessions.refetch(); }}>重新连接</button></div>;
  return <div className={`app-shell ${sidebarCollapsed ? "sidebar-collapsed" : ""} ${sidebarOpen ? "mobile-sidebar-open" : ""}`}>
    <div className="mobile-backdrop" onClick={() => setSidebarOpen(false)} />
    <SessionTabs projects={projects} sessionsByProject={workspace.sessionsByProject} selectedID={workspace.selectedID} selectedProjectID={workspace.selectedProjectID} contextUsage={workspace.contextUsage.data} creatingProject={pickingProject || workspace.createProject.isPending} creatingSessionProjectID={workspace.createSession.isPending ? workspace.createSession.variables : undefined} deletingSessionID={workspace.deleteSession.isPending ? workspace.deleteSession.variables?.session_id : undefined} runningSessionIDs={workspace.runningSessionIDs} onSelect={(id) => { workspace.select(id); setSidebarOpen(false); }} onSelectProject={workspace.selectProject} onDeleteSession={(session, label) => { setDeleteError(""); setSessionToDelete({ session, label }); }} onCreateProject={() => void createProject()} onCreateSession={(projectID) => { workspace.startSession(projectID); setSidebarOpen(false); }} />
    <section className="workspace">
      <header className="topbar"><button className="mobile-menu" onClick={() => setSidebarOpen(true)} aria-label="打开侧边栏"><Menu size={20} /></button><button className="collapse-button" onClick={() => setSidebarCollapsed((value) => !value)} aria-label={sidebarCollapsed ? "打开侧边栏" : "关闭侧边栏"}>{sidebarCollapsed ? <PanelLeftOpen size={19} /> : <PanelLeftClose size={19} />}</button><div className="workspace-meta"><strong title={workDir}>{isFirstRun ? "欢迎使用 LaxCode" : workDir || "请选择项目"}</strong><small>{isFirstRun ? <span>创建项目后即可开始</span> : <>{selected && <span>session_id: {selected.session_id}</span>}{workspace.createSession.isPending ? <span>正在创建会话…</span> : workspace.stream.running ? <><i className="working-dot" />正在执行</> : <span>准备就绪</span>}</>}</small></div><EvaluationPanel key={selected?.session_id ?? "no-session"} sessionID={selected?.session_id} workDir={workDir} disabled={workspace.stream.running || modelLocked} /></header>
      {isFirstRun ? <ProjectOnboarding cloud={cloud} creating={pickingProject || workspace.createProject.isPending || workspace.createSession.isPending} onCreate={() => void createProject()} /> : <>
        <MessageList messages={messages} sessionID={workspace.selectedID} hasMore={Boolean(workspace.history.hasNextPage)} loading={workspace.history.isFetchingNextPage} initialLoading={workspace.history.isLoading} error={workspace.stream.error} retryAction={workspace.stream.retryAction} retrying={workspace.stream.running} onRetry={workspace.retry} onLoadMore={() => workspace.history.fetchNextPage()} />
        <ModelPicker cloud={cloud} running={workspace.stream.running} bounceKey={gearBounce} />
        <ChatComposer running={workspace.stream.running} disabled={!workspace.selectedProject || !workspace.sessions.isSuccess || workspace.history.isLoading || workspace.createSession.isPending || (workspace.deleteSession.isPending && workspace.deleteSession.variables?.session_id === workspace.selectedID)} locked={modelLocked} onLockedClick={() => setGearBounce((value) => value + 1)} onSend={workspace.send} onCancel={workspace.cancel} />
      </>}
      {workspace.stream.approval && <ConfirmDialog key={workspace.stream.approval.approval_id} variant="approval" title={workspace.stream.approval.kind === "skill_write"
            ? "确认修改全局 Skill"
            : workspace.stream.approval.kind === "file_path"
              ? "确认访问工作目录之外的文件"
              : "确认危险命令"} description="LaxCode 已暂停执行，请检查操作内容后决定是否继续。" confirmLabel="允许并继续" cancelLabel="停止执行" pending={approvalSubmitting} error={approvalError} onConfirm={() => void answer(true)} onCancel={() => void answer(false)}>
        {workspace.stream.approval.command
          ? <><div className="approval-meta"><strong>{workspace.stream.approval.tool}</strong>{workspace.stream.approval.risk && <span>{workspace.stream.approval.risk}</span>}</div><CommandBlock key={workspace.stream.approval.approval_id} command={workspace.stream.approval.command} /></>
          : <p>{workspace.stream.approval.content}</p>}
      </ConfirmDialog>}
    </section>
    {creatingCloudProject && <CreateProjectDialog pending={workspace.createProject.isPending} onCancel={() => setCreatingCloudProject(false)} onCreate={async (name) => {
      await workspace.createProject.mutateAsync({ name });
      setCreatingCloudProject(false);
      setSidebarOpen(false);
      void models.refetch();
    }} />}
    {sessionToDelete && <ConfirmDialog variant="danger" title="删除这个会话？" description="会话历史和会话文件将被永久删除，此操作无法撤销。" confirmLabel="删除会话" cancelLabel="取消" pending={workspace.deleteSession.isPending} error={deleteError} onConfirm={() => void deleteSession()} onCancel={() => setSessionToDelete(undefined)}>
      <div className="confirm-session"><strong>{sessionToDelete.label}</strong><span>{sessionToDelete.session.work_dir}</span></div>
    </ConfirmDialog>}
  </div>;
}
