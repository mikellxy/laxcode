import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ClipboardCheck, LoaderCircle, X } from "lucide-react";
import { createEvaluation, listEvaluationJobs } from "../../api/evaluations";
import type { EvaluationJobStatus } from "../../types/api";

const statusLabel: Record<EvaluationJobStatus, string> = {
  queued: "排队中",
  running: "评估中",
  succeeded: "已完成",
  failed: "失败",
};

export function EvaluationPanel({ sessionID, workDir, disabled }: { sessionID?: string; workDir?: string; disabled: boolean }) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [requirement, setRequirement] = useState("");
  const jobs = useQuery({
    queryKey: ["evaluations", sessionID],
    enabled: Boolean(sessionID),
    queryFn: () => listEvaluationJobs(sessionID!),
    refetchInterval: (query) => query.state.data?.jobs.some((job) => job.status === "queued" || job.status === "running") ? 1500 : false,
  });
  const create = useMutation({
    mutationFn: () => createEvaluation(sessionID!, workDir!, requirement.trim()),
    onSuccess: async () => {
      setRequirement("");
      await client.invalidateQueries({ queryKey: ["evaluations", sessionID] });
    },
  });
  if (!sessionID || !workDir) return null;
  const hasActiveJob = jobs.data?.jobs.some((job) => job.status === "queued" || job.status === "running") ?? false;
  const canSubmit = !disabled && !hasActiveJob && !create.isPending && Boolean(requirement.trim());

  return <div className="evaluation-control">
    <button className={`evaluation-trigger ${open ? "active" : ""}`} onClick={() => setOpen((value) => !value)} aria-expanded={open} aria-label="评估当前会话">
      <ClipboardCheck size={15} /><span>评估</span>
    </button>
    {open && <section className="evaluation-panel" aria-label="评估任务">
      <header><div><strong>评估当前会话</strong><small>提交后由独立 Judge 异步执行</small></div><button onClick={() => setOpen(false)} aria-label="关闭评估面板"><X size={15} /></button></header>
      <textarea value={requirement} maxLength={65536} onChange={(event) => setRequirement(event.target.value)} placeholder="输入本次评估 requirement，例如重点检查测试覆盖和验收条件…" />
      {create.isError && <p className="evaluation-error">{create.error instanceof Error ? create.error.message : "创建评估任务失败"}</p>}
      <button className="evaluation-submit" disabled={!canSubmit} onClick={() => create.mutate()}>{create.isPending ? <><LoaderCircle className="spin" size={14} />提交中…</> : "开始评估"}</button>
      <div className="evaluation-list-heading"><strong>评估任务</strong><span>{jobs.data?.jobs.length ?? 0}</span></div>
      <div className="evaluation-list">
        {jobs.isLoading && <p className="evaluation-empty">加载中…</p>}
        {jobs.isError && <p className="evaluation-error">{jobs.error instanceof Error ? jobs.error.message : "加载任务失败"}</p>}
        {jobs.data?.jobs.map((job) => <article className="evaluation-job" key={job.id}>
          <div><span className={`evaluation-status ${job.status}`}>{statusLabel[job.status]}</span><time>{new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(job.created_at))}</time></div>
          <p>{job.requirement}</p>
          {job.report_path && <code title={job.report_path}>{job.report_path}</code>}
          {job.error && <small className="evaluation-error">{job.error}</small>}
        </article>)}
        {jobs.isSuccess && jobs.data.jobs.length === 0 && <p className="evaluation-empty">暂无评估任务</p>}
      </div>
    </section>}
  </div>;
}
