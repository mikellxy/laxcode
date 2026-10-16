import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { Activity, CircleAlert, LoaderCircle, RefreshCw, X } from "lucide-react";
import { ApiError } from "../../api/client";
import { getSessionPerformance } from "../../api/performance";
import { PerformanceSummaryView } from "./PerformanceSummaryView";
import "./performance.css";

export function PerformanceMonitor({ sessionID }: { sessionID?: string }) {
  const [open, setOpen] = useState(false);
  return <>
    <button type="button" className="performance-trigger" disabled={!sessionID} title={sessionID ? "查看当前会话的性能统计" : "请先选择一个会话"} aria-haspopup="dialog" onClick={() => setOpen(true)}>
      <Activity size={16} /><span>性能监控</span>
    </button>
    {open && sessionID && createPortal(<PerformanceDialog sessionID={sessionID} onClose={() => setOpen(false)} />, document.body)}
  </>;
}

function PerformanceDialog({ sessionID, onClose }: { sessionID: string; onClose: () => void }) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const titleID = useId();
  const descriptionID = useId();
  const query = useQuery({
    queryKey: ["performance", sessionID],
    queryFn: ({ signal }) => getSessionPerformance(sessionID, signal),
    retry: false, gcTime: 0, refetchOnWindowFocus: false,
  });
  useEffect(() => {
    const dialog = dialogRef.current;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog?.showModal();
    closeRef.current?.focus();
    return () => { dialog?.close(); if (previousFocus?.isConnected) previousFocus.focus(); };
  }, []);
  const unavailable = query.error instanceof ApiError && query.error.code === "TRACE_LOG_UNAVAILABLE";

  return <dialog ref={dialogRef} className="confirm-dialog performance-dialog" aria-modal="true" aria-labelledby={titleID} aria-describedby={descriptionID} onCancel={(event) => { event.preventDefault(); onClose(); }}>
    <header className="confirm-header performance-header">
      <span className="confirm-icon" aria-hidden="true"><Activity size={24} /></span>
      <div><span className="confirm-eyebrow">性能监控</span><h2 id={titleID}>会话性能汇总</h2></div>
      <button ref={closeRef} type="button" className="confirm-close" aria-label="关闭性能监控" onClick={onClose}><X size={18} /></button>
      <p id={descriptionID}>按 Chat 汇总本地 tracing 日志<span className="performance-session" title={sessionID}>Session · {sessionID}</span></p>
    </header>
    <div className="performance-body" aria-busy={query.isFetching}>
      {query.isPending && <div className="performance-state" role="status"><LoaderCircle className="spin" size={26} /><strong>正在分析性能日志…</strong></div>}
      {query.isError && <div className="performance-notice error" role="alert"><CircleAlert size={19} /><div>
        <strong>{unavailable ? "暂无本地性能日志" : "性能数据加载失败"}</strong>
        <p>{query.error.message}</p>
      </div></div>}
      {query.data && <PerformanceSummaryView data={query.data} />}
    </div>
    <footer className="confirm-actions performance-actions">
      <p>耗时单位 ms · 首输出包含推理或正文<br />仅统计已落盘请求，恢复前的等待时间不计入耗时。</p>
      <button type="button" className="confirm-cancel" disabled={query.isFetching} onClick={() => void query.refetch()}>
        <RefreshCw size={14} className={query.isFetching ? "spin" : undefined} />{query.isFetching ? "加载中…" : "刷新数据"}
      </button>
      <button type="button" className="confirm-submit" onClick={onClose}>关闭</button>
    </footer>
  </dialog>;
}
