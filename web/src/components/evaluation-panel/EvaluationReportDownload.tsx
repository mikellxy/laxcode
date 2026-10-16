import { useState } from "react";
import { Download, LoaderCircle } from "lucide-react";
import { downloadEvaluationReport } from "../../api/evaluations";
import type { EvaluationJobDTO } from "../../types/api";

export function EvaluationReportDownload({ sessionID, job }: { sessionID: string; job: EvaluationJobDTO }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const download = async () => {
    if (pending) return;
    setPending(true); setError("");
    try {
      const blob = await downloadEvaluationReport(sessionID, job.id);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url; link.download = `${job.id}.md`;
      document.body.append(link); link.click(); link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "报告下载失败，请重试");
    } finally { setPending(false); }
  };
  if (job.status !== "succeeded") return null;
  return <>
    <button type="button" className="evaluation-download" onClick={() => void download()} disabled={pending}>
      {pending ? <LoaderCircle className="spin" size={14} /> : <Download size={14} />}{pending ? "正在下载…" : "下载评估报告"}
    </button>
    {error && <p className="evaluation-error" role="alert">{error}</p>}
  </>;
}
