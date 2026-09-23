import { LoaderCircle, MessageSquare, Plus } from "lucide-react";
import type { ContextData, SessionDTO } from "../../types/api";
import laxcodeLogo from "../../assets/laxcode-logo.png";
import { sessionLabel } from "./label";

type Props = { sessions: SessionDTO[]; selectedID?: string; contextUsage?: ContextData; hasMore: boolean; loadingMore: boolean; creating: boolean; onSelect: (id: string) => void; onCreate: () => void; onLoadMore: () => void };

export function SessionTabs({ sessions, selectedID, contextUsage, hasMore, loadingMore, creating, onSelect, onCreate, onLoadMore }: Props) {
  const used = contextUsage ? contextUsage.window_token.token_input + contextUsage.window_token.token_output : 0;
  const percent = contextUsage && contextUsage.context_window > 0 ? Math.round(used / contextUsage.context_window * 100) : 0;
  return <aside className="sidebar">
    <div className="brand"><img className="brand-logo" src={laxcodeLogo} alt="LaxCode" /></div>
    <button className="new-chat" onClick={onCreate} disabled={creating}>{creating ? <LoaderCircle className="spin" size={17} /> : <Plus size={17} />}新建会话</button>
    <div className="session-heading"><span>最近会话</span><span>{sessions.length}</span></div>
    <nav className="session-list" aria-label="会话列表">
      {sessions.map((session, index) => <button key={session.session_id} className={`session-tab ${selectedID === session.session_id ? "active" : ""}`} onClick={() => onSelect(session.session_id)}>
        <MessageSquare size={16} /><span><strong>{sessionLabel(session.title, index, sessions.length)}</strong><small>{new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(session.updated_at))}</small></span>
      </button>)}
      {hasMore && <button className="load-sessions" onClick={onLoadMore} disabled={loadingMore}>{loadingMore ? "正在加载…" : "加载更早会话"}</button>}
    </nav>
    <div className="context-usage" aria-label="上下文使用量">
      <strong>Context</strong>
      <span>{contextUsage ? `${new Intl.NumberFormat("en-US").format(used)} tokens` : "— tokens"}</span>
      <span>{contextUsage ? `${percent}% used` : "— used"}</span>
    </div>
    <div className="sidebar-foot"><span className="status-dot" /> 服务连接就绪</div>
  </aside>;
}
