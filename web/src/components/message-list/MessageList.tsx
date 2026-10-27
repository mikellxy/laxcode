import { Fragment, useLayoutEffect, useRef } from "react";
import { ArrowUp, CircleAlert, LoaderCircle, RotateCcw } from "lucide-react";
import type { ChatMessage } from "../../types/chat";
import type { RetryAction } from "../../types/api";
import type { StreamState } from "../../features/chat/reducer";
import { Logo } from "../logo/Logo";
import { MessageItem } from "../message-item/MessageItem";
import { ToolMessageBlock } from "../tool-message-block/ToolMessageBlock";
import { groupMessages } from "./group";

type Props = { messages: ChatMessage[]; sessionID?: string; hasMore: boolean; loading: boolean; initialLoading: boolean; error?: string; retryAction?: RetryAction; retrying: boolean; waitingFor?: StreamState["waitingFor"]; onRetry: () => void; onLoadMore: () => Promise<unknown> };
// 贴底判定容差：视口底部 60px 内视为"在底部"，容忍动量滚动与小幅布局变化。
const atBottomSlack = 60;
export function MessageList({ messages, sessionID, hasMore, loading, initialLoading, error, retryAction, retrying, waitingFor, onRetry, onLoadMore }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null); const restore = useRef<{ height: number; top: number }>(); const atBottom = useRef(true); const sessionRef = useRef(sessionID);
  const waiting = retrying && !error ? waitingFor : undefined;
  const visibleMessages = waiting ? messages.filter((message) => message.role !== "assistant" || message.status !== "streaming" || Boolean(message.content.trim() || message.reasoningContent?.trim())) : messages;
  const lastUser = waiting ? [...visibleMessages].reverse().find((message) => message.role === "user") : undefined;
  const waitingStatus = waiting && <div className="sandbox-wait" role="status" aria-live="polite" aria-atomic="true"><span className="typing" aria-hidden="true"><i /><i /><i /></span><span>{waiting === "sandbox" ? "正在进入您的专属安全沙箱…" : "已进入您的专属安全沙箱，正在准备回复…"}</span></div>;
  // 贴底跟随：流式 chunk 原地更新末条消息、数组长度不变，故跟随依赖整个
  // messages；用户上翻离开底部即停止跟随，滚回底部自动恢复；会话切换重置
  // 为贴底，让新会话落到最新消息。behavior "instant" 显式绕过容器的 CSS
  // scroll-behavior: smooth，避免流式期间逐帧平滑动画堆积。加载更早消息的
  // 位置恢复优先于跟随。
  const trackBottom = () => { const el = scrollRef.current; if (el) atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < atBottomSlack; };
  useLayoutEffect(() => {
    const el = scrollRef.current; if (!el) return;
    if (sessionRef.current !== sessionID) { sessionRef.current = sessionID; atBottom.current = true; }
    if (restore.current) { el.scrollTo({ top: el.scrollHeight - restore.current.height + restore.current.top, behavior: "instant" }); restore.current = undefined; }
    else if (atBottom.current) el.scrollTo({ top: el.scrollHeight, behavior: "instant" });
  }, [messages, sessionID, waiting]);
  const load = async () => { const el = scrollRef.current; if (el) restore.current = { height: el.scrollHeight, top: el.scrollTop }; await onLoadMore(); };
  return <main className="message-scroll" ref={scrollRef} onScroll={trackBottom}>
    <div className="message-column">
      {hasMore && <button className="load-history" onClick={load} disabled={loading}>{loading ? <LoaderCircle className="spin" size={15} /> : <ArrowUp size={15} />}{loading ? "加载中…" : "查看更早消息"}</button>}
      {initialLoading ? <div className="center-state"><LoaderCircle className="spin" /><span>正在读取会话…</span></div> : visibleMessages.length === 0 && !error && !waiting ? <div className="empty-state"><span className="empty-state-logo"><Logo /></span><h2>从一个想法开始</h2><p>描述你想分析、构建或修改的内容。LaxCode 会在当前工作区中协助你完成。</p></div> : groupMessages(visibleMessages).map((block) => block.kind === "tools"
        ? <ToolMessageBlock key={block.messages[0].id} messages={block.messages} />
        : <Fragment key={block.message.id}><MessageItem message={block.message} sessionID={sessionID} />{block.message === lastUser && waitingStatus}</Fragment>)}
      {(!lastUser || initialLoading) && waitingStatus}
      {!error && !retrying && !initialLoading && retryAction === "resume" && <div className="retry-banner" role="status"><span className="retry-icon"><CircleAlert size={18} /></span><div><strong>上轮执行未完成</strong></div><button onClick={onRetry}><RotateCcw size={15} />继续执行</button></div>}
      {error && <div className="retry-banner" role="alert"><span className="retry-icon"><CircleAlert size={18} /></span><div><strong>本轮执行未完成</strong><p>{error}</p></div>{retryAction && <button onClick={onRetry} disabled={retrying}>{retrying ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}{retryAction === "resume" ? "继续执行" : "重新发送"}</button>}</div>}
    </div>
  </main>;
}
