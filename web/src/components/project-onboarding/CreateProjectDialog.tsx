import { useId, useRef, useState } from "react";
import { ConfirmDialog } from "../confirm-dialog/ConfirmDialog";

type Props = {
  pending: boolean;
  onCreate: (name: string) => Promise<void>;
  onCancel: () => void;
};

export function CreateProjectDialog({ pending, onCreate, onCancel }: Props) {
  const formID = useId();
  const inputID = useId();
  const hintID = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");

  const submit = async () => {
    if (pending) return;
    const trimmed = name.trim();
    const issue = !trimmed ? "请输入项目名称" : !/^[A-Za-z0-9_-]{1,120}$/.test(trimmed)
      ? "项目名称只能包含字母、数字、中横线或下划线，长度为 1–120 个字符"
      : ["agenthome", "tmp"].includes(trimmed.toLowerCase()) ? "项目名称不能为 agenthome 或 tmp" : "";
    if (issue) { setError(issue); inputRef.current?.focus(); return; }
    setError("");
    try { await onCreate(trimmed); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "项目创建失败，请重试"); }
  };

  return <ConfirmDialog variant="create" formID={formID} title="创建云端项目" description="为你的工作空间命名，创建后即可开始聊天。" confirmLabel="创建项目" cancelLabel="取消" pending={pending} error={error} onConfirm={() => void submit()} onCancel={onCancel}>
    <form id={formID} className="project-create-form" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
      <label htmlFor={inputID}>项目名称</label>
      <input ref={inputRef} id={inputID} name="project_name" value={name} onChange={(event) => { setName(event.target.value); setError(""); }} placeholder="例如 my-project" maxLength={120} autoComplete="off" spellCheck={false} disabled={pending} aria-required="true" aria-invalid={Boolean(error)} aria-describedby={hintID} />
      <p id={hintID}>支持字母、数字、中横线和下划线，最多 120 个字符。</p>
    </form>
  </ConfirmDialog>;
}
