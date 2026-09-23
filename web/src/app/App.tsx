import { useEffect, useMemo, useState } from "react";
import { Menu, PanelLeftClose, PanelLeftOpen, Sparkles } from "lucide-react";
import { getOrCreateGuestID } from "../features/identity/guest";
import { useWorkspace } from "../hooks/use-workspace";
import { historyToMessage } from "../types/chat";
import { SessionTabs } from "../components/session-tabs/SessionTabs";
import { MessageList } from "../components/message-list/MessageList";
import { ModelPicker } from "../components/model-picker/ModelPicker";
import { ChatComposer } from "../components/chat-composer/ChatComposer";
import { ProjectOnboarding } from "../components/project-onboarding/ProjectOnboarding";
import { pickDirectory } from "../api/directory-picker";

export function App() {
  const userID = useMemo(getOrCreateGuestID, []); const workspace = useWorkspace(userID); const [sidebarOpen, setSidebarOpen] = useState(false); const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [approvalSubmitting, setApprovalSubmitting] = useState(false); const [approvalError, setApprovalError] = useState("");
  const [pickingProject, setPickingProject] = useState(false);
  useEffect(() => { setApprovalError(""); setApprovalSubmitting(false); }, [workspace.stream.approval?.approval_id]);
  const answer = async (approved: boolean) => {
    setApprovalSubmitting(true); setApprovalError("");
    try { await workspace.approve(approved); } catch (error) { setApprovalError(error instanceof Error ? error.message : "确认提交失败"); }
    finally { setApprovalSubmitting(false); }
  };
  const createProject = async () => {
    if (pickingProject || workspace.createProject.isPending || workspace.createSession.isPending) return;
    setPickingProject(true);
    let projectCreated = false;
    try {
      const { path: workDir } = await pickDirectory();
      if (!workDir) return;
      const suggestedName = workDir.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || "新项目";
      const name = window.prompt("请输入项目名称", suggestedName)?.trim();
      if (!name) return;
      const project = await workspace.createProject.mutateAsync({ name, workDir });
      projectCreated = true;
      await workspace.createSession.mutateAsync(project.project_id);
      setSidebarOpen(false);
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      const fallback = projectCreated ? "项目已创建，但首个会话创建失败" : "项目创建失败";
      window.alert(error instanceof Error ? error.message : fallback);
    } finally {
      setPickingProject(false);
    }
  };
  const createSession = async (projectID: string) => {
    try {
      await workspace.createSession.mutateAsync(projectID);
      setSidebarOpen(false);
    } catch (error) {
      window.alert(error instanceof Error ? error.message : "会话创建失败");
    }
  };
  const messages = [...workspace.historyMessages.map(historyToMessage), ...workspace.stream.messages];
  const selected = workspace.allSessions.find((session) => session.session_id === workspace.selectedID);
  const projects = workspace.projects.data?.projects ?? [];
  const isFirstRun = workspace.projects.isSuccess && projects.length === 0;
  const connectionError = workspace.projects.error ?? workspace.sessions.error;
  if (workspace.projects.isError || workspace.sessions.isError) return <div className="fatal"><Sparkles /><h1>暂时无法连接 LaxCode</h1><p>{connectionError instanceof Error ? connectionError.message : "请确认 SSE 服务已启动。"}</p><button onClick={() => { void workspace.projects.refetch(); void workspace.sessions.refetch(); }}>重新连接</button></div>;
  return <div className={`app-shell ${sidebarCollapsed ? "sidebar-collapsed" : ""} ${sidebarOpen ? "mobile-sidebar-open" : ""}`}>
    <div className="mobile-backdrop" onClick={() => setSidebarOpen(false)} />
    <SessionTabs projects={projects} sessionsByProject={workspace.sessionsByProject} selectedID={workspace.selectedID} contextUsage={workspace.contextUsage.data} creatingProject={pickingProject || workspace.createProject.isPending} creatingSessionProjectID={workspace.createSession.isPending ? workspace.createSession.variables : undefined} onSelect={(id) => { workspace.select(id); setSidebarOpen(false); }} onCreateProject={() => void createProject()} onCreateSession={(projectID) => void createSession(projectID)} />
    <section className="workspace">
      <header className="topbar"><button className="mobile-menu" onClick={() => setSidebarOpen(true)} aria-label="打开侧边栏"><Menu size={20} /></button><button className="collapse-button" onClick={() => setSidebarCollapsed((value) => !value)} aria-label={sidebarCollapsed ? "打开侧边栏" : "关闭侧边栏"}>{sidebarCollapsed ? <PanelLeftOpen size={19} /> : <PanelLeftClose size={19} />}</button><div className="workspace-meta"><strong title={selected?.work_dir}>{isFirstRun ? "欢迎使用 LaxCode" : selected?.work_dir || "请选择会话"}</strong><small>{isFirstRun ? <span>创建项目后即可开始</span> : <><span>session_id: {selected?.session_id || "—"}</span>{workspace.stream.running ? <><i className="working-dot" />正在执行</> : <span>准备就绪</span>}</>}</small></div></header>
      {isFirstRun ? <ProjectOnboarding creating={pickingProject || workspace.createProject.isPending || workspace.createSession.isPending} onCreate={() => void createProject()} /> : <>
        <MessageList messages={messages} hasMore={Boolean(workspace.history.hasNextPage)} loading={workspace.history.isFetchingNextPage} initialLoading={workspace.history.isLoading} error={workspace.stream.error} retryAction={workspace.stream.retryAction} retrying={workspace.stream.running} onRetry={workspace.retry} onLoadMore={() => workspace.history.fetchNextPage()} />
        <ModelPicker running={workspace.stream.running} />
        <ChatComposer running={workspace.stream.running} disabled={!workspace.selectedID || workspace.history.isLoading} onSend={workspace.send} onCancel={workspace.cancel} />
      </>}
      {workspace.stream.approval && <div className="approval-backdrop"><div className="approval-dialog" role="dialog" aria-modal="true" aria-label="需要确认">
        <h2>{workspace.stream.approval.kind === "token_budget" ? "Token 预算已达到阈值" : "确认危险命令"}</h2>
        <p>{workspace.stream.approval.content}</p>
        {approvalError && <small className="approval-error">{approvalError}</small>}
        <div className="approval-actions"><button onClick={() => void answer(false)} disabled={approvalSubmitting}>停止</button><button onClick={() => void answer(true)} disabled={approvalSubmitting}>继续</button></div>
      </div></div>}
    </section>
  </div>;
}
