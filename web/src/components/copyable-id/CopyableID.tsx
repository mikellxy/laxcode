import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import "./copyable-id.css";

export function CopyableID({ label, value }: { label: "user_id" | "session_id"; value: string }) {
  const [feedback, setFeedback] = useState("");
  useEffect(() => {
    if (!feedback) return;
    const timer = setTimeout(() => setFeedback(""), 2500);
    return () => clearTimeout(timer);
  }, [feedback]);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setFeedback("已复制");
    } catch {
      setFeedback("复制失败，请手动选择复制");
    }
  };

  return <span className="copyable-id">
    <span className="copyable-id-label">{label}:</span>
    <span className="copyable-id-value" title={value}>{value}</span>
    <button type="button" className="copyable-id-button" aria-label={`复制 ${label}`} onClick={copy} data-feedback={Boolean(feedback)}>
      {feedback === "已复制" ? <Check size={16} aria-hidden="true" /> : <Copy size={16} aria-hidden="true" />}
      <span className="copyable-id-tooltip" aria-hidden="true">{feedback || "复制"}</span>
    </button>
    <span className="copyable-id-announcement" role="status">{feedback && `${label}：${feedback}`}</span>
  </span>;
}
