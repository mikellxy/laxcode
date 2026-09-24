import { memo } from "react";
import { Bot, CircleAlert, UserRound } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { ChatMessage } from "../../types/chat";
import { ReasoningPanel } from "../reasoning-panel/ReasoningPanel";

// MessageItem 渲染单条 user/assistant 消息；tool 消息由 MessageList 按归属
// 分组后交给 ToolMessageBlock 渲染。assistant 的 content 是 markdown 文本，
// 经 ReactMarkdown 渲染（基于 AST 生成 React 元素，不使用 innerHTML）；
// user 消息保持纯文本展示。memo 依赖消息对象的引用稳定：流式更新只重建
// 正在输出的那条消息，历史消息无需随每个 chunk 重新解析 markdown。
export const MessageItem = memo(function MessageItem({ message }: { message: ChatMessage }) {
  return <article className={`message ${message.role}`}>
    <div className="avatar">{message.role === "user" ? <UserRound size={17} /> : <Bot size={18} />}</div>
    <div className="message-body">
      <header><strong>{message.role === "user" ? "你" : "LaxCode"}</strong><time>{new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" }).format(new Date(message.createdAt))}</time></header>
      <ReasoningPanel content={message.reasoningContent ?? ""} streaming={message.reasoningStreaming} />
      {message.content && (message.role === "assistant"
        ? <div className="message-content markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{message.content}</ReactMarkdown></div>
        : <div className="message-content">{message.content}</div>)}
      {message.status === "streaming" && <span className="typing"><i /><i /><i /></span>}
      {message.status === "error" && <div className="message-error"><CircleAlert size={14} />生成中断</div>}
    </div>
  </article>;
});
