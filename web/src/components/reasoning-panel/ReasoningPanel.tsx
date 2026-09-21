import { Brain, ChevronDown } from "lucide-react";

export function ReasoningPanel({ content, streaming }: { content: string; streaming: boolean }) {
  if (!content) return null;
  return <details className="reasoning" open={streaming}><summary><Brain size={14} /><span>{streaming ? "正在思考" : "思考过程"}</span><ChevronDown size={14} /></summary><div>{content}</div></details>;
}
