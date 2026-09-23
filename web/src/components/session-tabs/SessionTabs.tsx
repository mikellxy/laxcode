import { useEffect, useState } from "react";
import { ChevronRight, Folder, LoaderCircle, MessageSquare, Plus } from "lucide-react";
import type { ContextData, ProjectDTO, SessionDTO } from "../../types/api";
import laxcodeLogo from "../../assets/laxcode-logo.png";
import { sessionLabel } from "./label";

type Props = {
  projects: ProjectDTO[];
  sessionsByProject: Record<string, SessionDTO[]>;
  selectedID?: string;
  contextUsage?: ContextData;
  creatingProject: boolean;
  creatingSessionProjectID?: string;
  onSelect: (id: string) => void;
  onCreateProject: () => void;
  onCreateSession: (projectID: string) => void;
};

export function SessionTabs({ projects, sessionsByProject, selectedID, contextUsage, creatingProject, creatingSessionProjectID, onSelect, onCreateProject, onCreateSession }: Props) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  useEffect(() => {
    setExpanded((current) => {
      const next = new Set(current);
      projects.forEach((project) => next.add(project.project_id));
      return next;
    });
  }, [projects]);

  const selectedProjectID = projects.find((project) =>
    (sessionsByProject[project.project_id] ?? []).some((session) => session.session_id === selectedID))?.project_id;
  const used = contextUsage ? contextUsage.window_token.token_input + contextUsage.window_token.token_output : 0;
  const percent = contextUsage && contextUsage.context_window > 0 ? Math.round(used / contextUsage.context_window * 100) : 0;
  const toggle = (projectID: string) => setExpanded((current) => {
    const next = new Set(current);
    if (next.has(projectID)) next.delete(projectID); else next.add(projectID);
    return next;
  });

  return <aside className="sidebar">
    <div className="brand"><img className="brand-logo" src={laxcodeLogo} alt="LaxCode" /></div>
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
          <div className={`project-row ${isActive ? "active" : ""}`} title={project.work_dir}>
            <button className="project-toggle" onClick={() => toggle(project.project_id)} aria-expanded={isExpanded}>
              <ChevronRight className={isExpanded ? "expanded" : ""} size={15} />
              <Folder size={16} />
              <span>{project.name}</span>
            </button>
            <button className="project-new-session" onClick={() => onCreateSession(project.project_id)} disabled={creatingSessionProjectID === project.project_id} aria-label={`在 ${project.name} 中新建会话`} title="新会话">
              {creatingSessionProjectID === project.project_id ? <LoaderCircle className="spin" size={14} /> : <Plus size={15} />}
              <span>新会话</span>
            </button>
          </div>
          {isExpanded && <div className="project-sessions">
            {projectSessions.map((session, index) => <button key={session.session_id} className={`session-tab ${selectedID === session.session_id ? "active" : ""}`} onClick={() => onSelect(session.session_id)}>
              <MessageSquare size={14} /><span><strong>{sessionLabel(session.title, index, projectSessions.length)}</strong><small>{new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(session.updated_at))}</small></span>
            </button>)}
            {projectSessions.length === 0 && <p className="project-empty">暂无会话</p>}
          </div>}
        </section>;
      })}
      {projects.length === 0 && <p className="project-list-empty">点击“新项目”开始</p>}
    </nav>
    <div className="context-usage" aria-label="上下文使用量">
      <strong>Context</strong>
      <span>{contextUsage ? `${new Intl.NumberFormat("en-US").format(used)} tokens` : "— tokens"}</span>
      <span>{contextUsage ? `${percent}% used` : "— used"}</span>
    </div>
    <div className="sidebar-foot"><span className="status-dot" /> 服务连接就绪</div>
  </aside>;
}
