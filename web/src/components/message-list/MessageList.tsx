import { useLayoutEffect, useRef } from "react";
import { ArrowUp, CircleAlert, LoaderCircle, RotateCcw } from "lucide-react";
import type { ChatMessage } from "../../types/chat";
import type { RetryAction } from "../../types/api";
import laxcodeLogo from "../../assets/laxcode-logo.png";
import { MessageItem } from "../message-item/MessageItem";

type Props = { messages: ChatMessage[]; hasMore: boolean; loading: boolean; initialLoading: boolean; error?: string; retryAction?: RetryAction; retrying: boolean; onRetry: () => void; onLoadMore: () => Promise<unknown> };
export function MessageList({ messages, hasMore, loading, initialLoading, error, retryAction, retrying, onRetry, onLoadMore }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null); const bottomRef = useRef<HTMLDivElement>(null); const prevLength = useRef(0); const restore = useRef<{ height: number; top: number }>();
  useLayoutEffect(() => { const el = scrollRef.current; if (!el) return; if (restore.current) { el.scrollTop = el.scrollHeight - restore.current.height + restore.current.top; restore.current = undefined; } else if (messages.length > prevLength.current) bottomRef.current?.scrollIntoView({ behavior: prevLength.current ? "smooth" : "auto" }); prevLength.current = messages.length; }, [messages.length]);
  const load = async () => { const el = scrollRef.current; if (el) restore.current = { height: el.scrollHeight, top: el.scrollTop }; await onLoadMore(); };
  return <main className="message-scroll" ref={scrollRef}>
    <div className="message-column">
      {hasMore && <button className="load-history" onClick={load} disabled={loading}>{loading ? <LoaderCircle className="spin" size={15} /> : <ArrowUp size={15} />}{loading ? "加载中…" : "查看更早消息"}</button>}
      {initialLoading ? <div className="center-state"><LoaderCircle className="spin" /><span>正在读取会话…</span></div> : messages.length === 0 && !error ? <div className="empty-state"><span className="empty-state-logo"><img src={laxcodeLogo} alt="LaxCode" /></span><h2>从一个想法开始</h2><p>描述你想分析、构建或修改的内容。LaxCode 会在当前工作区中协助你完成。</p></div> : messages.map((message) => <MessageItem key={message.id} message={message} />)}
      {error && <div className="retry-banner" role="alert"><span className="retry-icon"><CircleAlert size={18} /></span><div><strong>本轮执行未完成</strong><p>{error}</p></div>{retryAction && <button onClick={onRetry} disabled={retrying}>{retrying ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}{retryAction === "resume" ? "继续执行" : "重新发送"}</button>}</div>}
      <div ref={bottomRef} />
    </div>
  </main>;
}
