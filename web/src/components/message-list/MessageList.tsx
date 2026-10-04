import { useLayoutEffect, useRef } from "react";
import { ArrowUp, CircleAlert, LoaderCircle, RotateCcw } from "lucide-react";
import type { ChatMessage } from "../../types/chat";
import type { RetryAction } from "../../types/api";
import laxcodeLogo from "../../assets/laxcode-logo.png";
import { MessageItem } from "../message-item/MessageItem";
import { ToolMessageBlock } from "../tool-message-block/ToolMessageBlock";
import { groupMessages } from "./group";

type Props = { messages: ChatMessage[]; sessionID?: string; hasMore: boolean; loading: boolean; initialLoading: boolean; error?: string; retryAction?: RetryAction; retrying: boolean; onRetry: () => void; onLoadMore: () => Promise<unknown> };
// 贴底判定容差：视口底部 60px 内视为"在底部"，容忍动量滚动与小幅布局变化。
const atBottomSlack = 60;
export function MessageList({ messages, sessionID, hasMore, loading, initialLoading, error, retryAction, retrying, onRetry, onLoadMore }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null); const restore = useRef<{ height: number; top: number }>(); const atBottom = useRef(true); const sessionRef = useRef(sessionID);
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
  }, [messages, sessionID]);
  const load = async () => { const el = scrollRef.current; if (el) restore.current = { height: el.scrollHeight, top: el.scrollTop }; await onLoadMore(); };
  return <main className="message-scroll" ref={scrollRef} onScroll={trackBottom}>
    <div className="message-column">
      {hasMore && <button className="load-history" onClick={load} disabled={loading}>{loading ? <LoaderCircle className="spin" size={15} /> : <ArrowUp size={15} />}{loading ? "加载中…" : "查看更早消息"}</button>}
      {initialLoading ? <div className="center-state"><LoaderCircle className="spin" /><span>正在读取会话…</span></div> : messages.length === 0 && !error ? <div className="empty-state"><span className="empty-state-logo"><img src={laxcodeLogo} alt="LaxCode" /></span><h2>从一个想法开始</h2><p>描述你想分析、构建或修改的内容。LaxCode 会在当前工作区中协助你完成。</p></div> : groupMessages(messages).map((block) => block.kind === "tools"
        ? <ToolMessageBlock key={block.messages[0].id} messages={block.messages} />
        : <MessageItem key={block.message.id} message={block.message} />)}
      {error && <div className="retry-banner" role="alert"><span className="retry-icon"><CircleAlert size={18} /></span><div><strong>本轮执行未完成</strong><p>{error}</p></div>{retryAction && <button onClick={onRetry} disabled={retrying}>{retrying ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}{retryAction === "resume" ? "继续执行" : "重新发送"}</button>}</div>}
    </div>
  </main>;
}
