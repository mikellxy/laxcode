import { useId, useState } from "react";
import { ConfirmDialog } from "../confirm-dialog/ConfirmDialog";

type Props = { initialTitle: string; pending: boolean; onSave: (title: string) => Promise<void>; onCancel: () => void };

export function RenameSessionDialog({ initialTitle, pending, onSave, onCancel }: Props) {
  const formID = useId();
  const inputID = useId();
  const hintID = useId();
  const [title, setTitle] = useState(initialTitle);
  const [error, setError] = useState("");
  const submit = async () => {
    if (pending) return;
    const trimmed = title.trim();
    if (!trimmed || Array.from(trimmed).length > 120) { setError("会话名称长度应为 1–120 个字符"); return; }
    setError("");
    try { await onSave(trimmed); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "重命名失败，请重试"); }
  };
  return <ConfirmDialog variant="rename" formID={formID} title="重命名会话" description="为这段对话起一个容易辨认的名称。" confirmLabel="保存" cancelLabel="取消" pending={pending} error={error} onConfirm={() => void submit()} onCancel={onCancel}>
    <form id={formID} className="project-create-form" onSubmit={event => { event.preventDefault(); void submit(); }}>
      <label htmlFor={inputID}>会话名称</label>
      <input id={inputID} name="session_title" value={title} onChange={event => { setTitle(event.target.value); setError(""); }} autoComplete="off" disabled={pending} aria-required="true" aria-invalid={Boolean(error)} aria-describedby={hintID} />
      <p id={hintID}>最多 120 个字符，较长名称将在列表中以省略号显示。</p>
    </form>
  </ConfirmDialog>;
}
