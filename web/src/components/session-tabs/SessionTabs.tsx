import { useEffect, useState } from "react";
import { ChevronRight, Folder, LoaderCircle, MessageSquare, Pencil, Plus, Trash2 } from "lucide-react";
import type { ContextData, ProjectDTO, SessionDTO } from "../../types/api";
import { Logo } from "../logo/Logo";
import { contextUsageLabel, sessionLabel, workDirLabel } from "./label";
import { PerformanceMonitor } from "../performance-monitor/PerformanceMonitor";

type Props = {
  projects: ProjectDTO[];
  sessionsByProject: Record<string, SessionDTO[]>;
  selectedID?: string;
  selectedProjectID?: string;
  contextUsage?: ContextData;
  creatingProject: boolean;
  creatingSessionProjectID?: string;
  deletingSessionID?: string;
  renamingSessionID?: string;
  loadingSessions?: boolean;
  runningSessionIDs: string[];
  onSelect: (id: string) => void;
  onSelectProject: (projectID: string) => void;
  onDeleteSession: (session: SessionDTO, label: string) => void;
  onRenameSession: (session: SessionDTO, label: string) => void;
  onCreateProject: () => void;
  onCreateSession: (projectID: string) => void;
};

export function SessionTabs({ projects, sessionsByProject, selectedID, selectedProjectID, contextUsage, creatingProject, creatingSessionProjectID, deletingSessionID, renamingSessionID, loadingSessions, runningSessionIDs, onSelect, onSelectProject, onDeleteSession, onRenameSession, onCreateProject, onCreateSession }: Props) {
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(projects.map(project => project.project_id)));
  useEffect(() => {
    setExpanded((current) => {
      const next = new Set(current);
      projects.forEach((project) => next.add(project.project_id));
      return next;
    });
  }, [projects]);

  const used = contextUsage ? contextUsage.window_token.token_input + contextUsage.window_token.token_output : 0;
  const toggle = (projectID: string) => setExpanded((current) => {
    const next = new Set(current);
    if (next.has(projectID)) next.delete(projectID); else next.add(projectID);
    return next;
  });

  return <aside className="sidebar">
    <div className="brand"><Logo className="brand-logo" /></div>
    <button className="new-project" onClick={onCreateProject} disabled={creatingProject}>
      {creatingProject ? <LoaderCircle className="spin" size={17} /> : <Plus size={17} />}新项目
    </button>
    <div className="session-heading"><span>项目</span><span>{projects.length}</span></div>
    <nav className="project-list" aria-label="项目和会话列表">
      {projects.map((project) => {
        const projectSessions = sessionsByProject[project.project_id] ?? [];
        const isExpanded = expanded.has(project.project_id);
        const isActive = selectedProjectID === project.project_id;
        return <section key={project.project_id} className={`project-group ${isActive ? "active" : ""}`}>
          <div className={`project-row ${isActive ? "active" : ""}`} title={workDirLabel(project.name)}>
            <button className="project-toggle" onClick={() => { toggle(project.project_id); onSelectProject(project.project_id); }} aria-expanded={isExpanded}>
              <ChevronRight className={isExpanded ? "expanded" : ""} size={15} />
              <Folder size={16} />
              <span>{project.name}</span>
            </button>
            <button className="project-new-session" onClick={() => onCreateSession(project.project_id)} disabled={Boolean(creatingSessionProjectID) || loadingSessions} aria-label={`在 ${project.name} 中新建会话`} title="新会话">
              {creatingSessionProjectID === project.project_id ? <LoaderCircle className="spin" size={14} /> : <Plus size={15} />}
              <span>新会话</span>
            </button>
          </div>
          {isExpanded && <div className="project-sessions">
            {projectSessions.map((session, index) => {
              const label = sessionLabel(session.title, index, projectSessions.length);
              return <div key={session.session_id} className={`session-row ${selectedID === session.session_id ? "active" : ""}`}>
                <button className="session-tab" title={label} onClick={() => onSelect(session.session_id)} aria-current={selectedID === session.session_id ? "page" : undefined}>
                  <MessageSquare size={14} /><span><strong>{label}</strong><small>{new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(session.updated_at))}</small></span>
                </button>
                <div className="session-actions">
                <button className="session-rename" onClick={() => onRenameSession(session, label)} disabled={Boolean(renamingSessionID) || deletingSessionID === session.session_id} aria-label={`重命名会话 ${label}`} title="重命名会话">
                  {renamingSessionID === session.session_id ? <LoaderCircle className="spin" size={14} /> : <Pencil size={14} />}
                </button>
                <button className="session-delete" onClick={() => onDeleteSession(session, label)} disabled={Boolean(deletingSessionID) || renamingSessionID === session.session_id || runningSessionIDs.includes(session.session_id)} aria-label={`删除会话 ${label}`} title={runningSessionIDs.includes(session.session_id) ? "会话正在执行" : "删除会话"}>
                  {deletingSessionID === session.session_id ? <LoaderCircle className="spin" size={14} /> : <Trash2 size={14} />}
                </button>
                </div>
              </div>;
            })}
            {loadingSessions && projectSessions.length === 0 ? <p className="project-empty">正在加载会话…</p> : projectSessions.length === 0 && <div className={`session-row ${isActive && !selectedID ? "active" : ""}`}>
              <button className="session-tab" onClick={() => onSelectProject(project.project_id)} aria-current={isActive && !selectedID ? "page" : undefined}>
                <MessageSquare size={14} /><span><strong>新对话</strong><small>发送消息即可开始</small></span>
              </button>
            </div>}
          </div>}
        </section>;
      })}
      {projects.length === 0 && <p className="project-list-empty">点击“新项目”开始</p>}
    </nav>
    <div className="context-usage" aria-label="上下文使用量">
      <strong>Context</strong>
      <span>{contextUsage ? `${new Intl.NumberFormat("en-US").format(used)} tokens` : "— tokens"}</span>
      <span>{contextUsage ? contextUsageLabel(used, contextUsage.context_window) : "— used"}</span>
    </div>
    <PerformanceMonitor key={selectedID ?? "no-session"} sessionID={selectedID} />
    <div className="sidebar-foot"><span className="status-dot" /> 服务连接就绪</div>
  </aside>;
}
