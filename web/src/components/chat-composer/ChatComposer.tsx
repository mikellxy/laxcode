import { useEffect, useRef, useState } from "react";
import { ArrowUp, Square } from "lucide-react";
import { shouldSubmitOnKeyDown } from "./keyboard";

export function ChatComposer({ running, disabled, onSend, onCancel }: { running: boolean; disabled: boolean; onSend: (value: string) => void; onCancel: () => void }) {
  const [value, setValue] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);
  const composing = useRef(false);
  useEffect(() => { const el = ref.current; if (el) { el.style.height = "0"; el.style.height = `${Math.min(el.scrollHeight, 160)}px`; } }, [value]);
  const submit = () => { const task = value.trim(); if (!task || running || disabled) return; setValue(""); onSend(task); };
  return <div className="composer-wrap"><div className={`composer ${running ? "running" : ""}`}><textarea
    ref={ref}
    rows={1}
    value={value}
    disabled={disabled}
    placeholder="给 LaxCode 发送任务…"
    onChange={(event) => setValue(event.target.value)}
    onCompositionStart={() => { composing.current = true; }}
    onCompositionEnd={() => { composing.current = false; }}
    onKeyDown={(event) => {
      if (!shouldSubmitOnKeyDown({
        key: event.key,
        shiftKey: event.shiftKey,
        isComposing: composing.current || event.nativeEvent.isComposing,
        keyCode: event.nativeEvent.keyCode,
      })) return;
      event.preventDefault();
      submit();
    }}
  /><div className="composer-actions"><span>Enter 发送 · Shift + Enter 换行</span>{running ? <button className="stop-button" onClick={onCancel} aria-label="取消生成"><Square size={14} fill="currentColor" /></button> : <button className="send-button" onClick={submit} disabled={!value.trim() || disabled} aria-label="发送"><ArrowUp size={18} /></button>}</div></div><p>LaxCode 可能会犯错，请检查重要结果。</p></div>;
}
