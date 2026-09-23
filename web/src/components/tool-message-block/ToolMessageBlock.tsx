import { useState } from "react";
import { ChevronDown, Terminal } from "lucide-react";
import type { ChatMessage } from "../../types/chat";

// ToolMessageBlock 把同属一个 assistant 消息的多次工具调用合并为一个标签块：
// 默认折叠——每条 tool 消息占一行，超宽内容以省略号截断（悬停可看完整摘要）；
// 点击头部展开为原本的完整消息（自动换行），再次点击折叠回单行视图。
export function ToolMessageBlock({ messages }: { messages: ChatMessage[] }) {
  const [expanded, setExpanded] = useState(false);
  return <div className={`tool-message${expanded ? " expanded" : ""}`}>
    <button type="button" className="tool-message-toggle" aria-expanded={expanded} onClick={() => setExpanded((value) => !value)}>
      <Terminal size={15} />
      <span>工具调用{messages.length > 1 ? ` × ${messages.length}` : ""}</span>
      <ChevronDown size={14} />
    </button>
    {messages.map((message) => (
      <div className="tool-message-row" key={message.id}>
        <code title={expanded ? undefined : message.toolSummary || "调用工具"}>{message.toolSummary || "调用工具"}</code>
      </div>
    ))}
  </div>;
}
