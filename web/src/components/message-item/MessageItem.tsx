import { Bot, CircleAlert, UserRound } from "lucide-react";
import type { ChatMessage } from "../../types/chat";
import { ReasoningPanel } from "../reasoning-panel/ReasoningPanel";

// MessageItem 渲染单条 user/assistant 消息；tool 消息由 MessageList 按归属
// 分组后交给 ToolMessageBlock 渲染。
export function MessageItem({ message }: { message: ChatMessage }) {
  return <article className={`message ${message.role}`}>
    <div className="avatar">{message.role === "user" ? <UserRound size={17} /> : <Bot size={18} />}</div>
    <div className="message-body">
      <header><strong>{message.role === "user" ? "你" : "LaxCode"}</strong><time>{new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" }).format(new Date(message.createdAt))}</time></header>
      <ReasoningPanel content={message.reasoningContent ?? ""} streaming={message.reasoningStreaming} />
      {message.content && <div className="message-content">{message.content}</div>}
      {message.status === "streaming" && <span className="typing"><i /><i /><i /></span>}
      {message.status === "error" && <div className="message-error"><CircleAlert size={14} />生成中断</div>}
    </div>
  </article>;
}
