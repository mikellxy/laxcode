import { useEffect, useMemo, useState } from "react";
import { Menu, PanelLeftClose, PanelLeftOpen, Sparkles } from "lucide-react";
import { getOrCreateGuestID } from "../features/identity/guest";
import { useWorkspace } from "../hooks/use-workspace";
import { historyToMessage } from "../types/chat";
import { SessionTabs } from "../components/session-tabs/SessionTabs";
import { MessageList } from "../components/message-list/MessageList";
import { ModelPicker } from "../components/model-picker/ModelPicker";
import { ChatComposer } from "../components/chat-composer/ChatComposer";

export function App() {
  const userID = useMemo(getOrCreateGuestID, []); const workspace = useWorkspace(userID); const [sidebarOpen, setSidebarOpen] = useState(false); const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [approvalSubmitting, setApprovalSubmitting] = useState(false); const [approvalError, setApprovalError] = useState("");
  useEffect(() => { setApprovalError(""); setApprovalSubmitting(false); }, [workspace.stream.approval?.approval_id]);
  const answer = async (approved: boolean) => {
    setApprovalSubmitting(true); setApprovalError("");
    try { await workspace.approve(approved); } catch (error) { setApprovalError(error instanceof Error ? error.message : "确认提交失败"); }
    finally { setApprovalSubmitting(false); }
  };
  const createSession = () => {
    const storageKey = "laxcode.workDir";
    const value = window.prompt("请输入新会话的工作目录", window.localStorage.getItem(storageKey) ?? "")?.trim();
    if (!value) return;
    window.localStorage.setItem(storageKey, value);
    workspace.create.mutate(value);
    setSidebarOpen(false);
  };
  const messages = [...workspace.historyMessages.map(historyToMessage), ...workspace.stream.messages];
  const selected = workspace.allSessions.find((session) => session.session_id === workspace.selectedID);
  if (workspace.sessions.isError) return <div className="fatal"><Sparkles /><h1>暂时无法连接 LaxCode</h1><p>{workspace.sessions.error instanceof Error ? workspace.sessions.error.message : "请确认 SSE 服务已启动。"}</p><button onClick={() => workspace.sessions.refetch()}>重新连接</button></div>;
  return <div className={`app-shell ${sidebarCollapsed ? "sidebar-collapsed" : ""} ${sidebarOpen ? "mobile-sidebar-open" : ""}`}>
    <div className="mobile-backdrop" onClick={() => setSidebarOpen(false)} />
    <SessionTabs sessions={workspace.allSessions} selectedID={workspace.selectedID} contextUsage={workspace.contextUsage.data} hasMore={Boolean(workspace.sessions.hasNextPage)} loadingMore={workspace.sessions.isFetchingNextPage} creating={workspace.create.isPending} onSelect={(id) => { workspace.select(id); setSidebarOpen(false); }} onCreate={createSession} onLoadMore={() => workspace.sessions.fetchNextPage()} />
    <section className="workspace">
      <header className="topbar"><button className="mobile-menu" onClick={() => setSidebarOpen(true)} aria-label="打开侧边栏"><Menu size={20} /></button><button className="collapse-button" onClick={() => setSidebarCollapsed((value) => !value)} aria-label={sidebarCollapsed ? "打开侧边栏" : "关闭侧边栏"}>{sidebarCollapsed ? <PanelLeftOpen size={19} /> : <PanelLeftClose size={19} />}</button><div><strong>{selected?.title || "新会话"}</strong><small>{workspace.stream.running ? <><i className="working-dot" />正在执行</> : "准备就绪"}</small></div><span className="session-code">{workspace.selectedID?.slice(0, 8)}</span></header>
      <MessageList messages={messages} hasMore={Boolean(workspace.history.hasNextPage)} loading={workspace.history.isFetchingNextPage} initialLoading={workspace.history.isLoading} error={workspace.stream.error} retryAction={workspace.stream.retryAction} retrying={workspace.stream.running} onRetry={workspace.retry} onLoadMore={() => workspace.history.fetchNextPage()} />
      <ModelPicker running={workspace.stream.running} />
      <ChatComposer running={workspace.stream.running} disabled={!workspace.selectedID || workspace.history.isLoading} onSend={workspace.send} onCancel={workspace.cancel} />
      {workspace.stream.approval && <div className="approval-backdrop"><div className="approval-dialog" role="dialog" aria-modal="true" aria-label="需要确认">
        <h2>{workspace.stream.approval.kind === "token_budget" ? "Token 预算已达到阈值" : "确认危险命令"}</h2>
        <p>{workspace.stream.approval.content}</p>
        {approvalError && <small className="approval-error">{approvalError}</small>}
        <div className="approval-actions"><button onClick={() => void answer(false)} disabled={approvalSubmitting}>停止</button><button onClick={() => void answer(true)} disabled={approvalSubmitting}>继续</button></div>
      </div></div>}
    </section>
  </div>;
}
