import { CircleAlert } from "lucide-react";
import type { DurationStats, PerformanceSummary } from "../../types/api";

const number = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 });
const milliseconds = new Intl.NumberFormat("zh-CN", { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const seconds = new Intl.NumberFormat("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const formatDuration = (value: number) => value > 60_000
  ? { amount: seconds.format(value / 1000), unit: "s" }
  : { amount: milliseconds.format(value), unit: "ms" };
const durationText = (value: number | null) => {
  if (value === null) return "—";
  const { amount, unit } = formatDuration(value);
  return `${amount} ${unit}`;
};
const time = (value: string) => new Date(value).toLocaleString("zh-CN", { hour12: false });

function CallStats({ llm, tools, label }: { llm: DurationStats; tools: Record<string, DurationStats>; label: string }) {
  const rows: [string, DurationStats][] = [["LLM generate", llm], ...Object.entries(tools).sort(([a], [b]) => a.localeCompare(b))];
  return <div className="performance-table-scroll" tabIndex={0} role="region" aria-label={label}>
    <table className="performance-table">
      <caption>{label}</caption>
      <thead><tr><th scope="col">调用类型</th><th scope="col">次数</th><th scope="col">总耗时</th><th scope="col">平均耗时</th></tr></thead>
      <tbody>{rows.map(([name, stats]) => <tr key={name}>
        <th scope="row">{name}</th><td>{number.format(stats.count)}</td><td>{durationText(stats.total_ms)}</td><td>{stats.count ? durationText(stats.avg_ms) : "—"}</td>
      </tr>)}</tbody>
    </table>
  </div>;
}

export function PerformanceSummaryView({ data }: { data: PerformanceSummary }) {
  const total = formatDuration(data.chats.reduce((sum, chat) => sum + chat.total_ms, 0));
  return <>
    {data.partial && <div className="performance-notice" role="status"><CircleAlert size={17} /><div>
      <strong>当前统计不完整</strong>
      <p>{data.invalid_line_count} 行日志无效，{data.incomplete_trace_count} 条 Trace 缺少请求根记录。正在执行的请求可能尚未落盘，请稍后刷新。</p>
    </div></div>}
    {data.chats.length === 0 ? <div className="performance-state"><strong>暂无已完成的请求记录</strong><p>首次对话执行结束并写入 tracing 日志后，可刷新查看。</p></div> : <>
      <dl className="performance-overview">
        <div><dt>Chat 轮数</dt><dd>{number.format(data.chats.length)}<small>轮</small></dd></div>
        <div><dt>累计执行耗时</dt><dd>{total.amount}<small>{total.unit}</small></dd></div>
        <div><dt>LLM 调用次数</dt><dd>{number.format(data.chats.reduce((sum, chat) => sum + chat.llm_generate.count, 0))}<small>次</small></dd></div>
      </dl>
      <div className="performance-chats">{data.chats.map((chat, index) => <article className="performance-chat" key={chat.chat_id}>
        <header><h3>第 {index + 1} 轮 Chat</h3><code title={chat.chat_id}>{chat.chat_id}</code></header>
        <dl className="performance-metrics">
          <div><dt>总耗时</dt><dd>{durationText(chat.total_ms)}</dd></div>
          <div><dt>首次请求首输出</dt><dd>{durationText(chat.requests[0]?.first_output_ms ?? null)}</dd></div>
          <div><dt>LLM 平均耗时</dt><dd>{chat.llm_generate.count ? durationText(chat.llm_generate.avg_ms) : "—"}</dd></div>
        </dl>
        <CallStats llm={chat.llm_generate} tools={chat.tools} label={`第 ${index + 1} 轮 Chat 调用统计`} />
        <details className="performance-requests">
          <summary>请求明细 · {chat.requests.length} 次</summary>
          {chat.requests.map((request) => <section className="performance-request" key={request.trace_id} aria-label={`${request.operation} 请求`}>
            <header><strong>{request.operation === "send" ? "发送" : request.operation === "resume" ? "恢复" : request.operation}</strong>
              <span className={`performance-status ${request.status}`}>{request.status === "error" ? "执行异常" : "执行完成"}</span>
              <time dateTime={request.start_time}>{time(request.start_time)}</time>
            </header>
            <dl className="performance-metrics">
              <div><dt>请求耗时</dt><dd>{durationText(request.total_ms)}</dd></div>
              <div><dt>首输出耗时</dt><dd>{durationText(request.first_output_ms)}</dd></div>
              <div><dt>LLM 平均耗时</dt><dd>{request.llm_generate.count ? durationText(request.llm_generate.avg_ms) : "—"}</dd></div>
            </dl>
            {chat.requests.length > 1 && <CallStats llm={request.llm_generate} tools={request.tools} label={`${request.operation} 请求调用统计`} />}
            <p className="performance-identifiers">Request: <code>{request.request_id || "—"}</code><br />Trace: <code>{request.trace_id}</code></p>
          </section>)}
        </details>
      </article>)}</div>
    </>}
  </>;
}
