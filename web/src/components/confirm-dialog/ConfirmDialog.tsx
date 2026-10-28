import { useEffect, useId, useRef, type ReactNode } from "react";
import { CircleAlert, FolderPlus, LoaderCircle, Pencil, Trash2, X } from "lucide-react";

type Props = {
  title: string;
  description: string;
  children?: ReactNode;
  variant: "danger" | "create" | "rename";
  formID?: string;
  confirmLabel: string;
  cancelLabel: string;
  pending: boolean;
  error?: string;
  onConfirm: () => void;
  onCancel: () => void;
};

export function ConfirmDialog({ title, description, children, variant, formID, confirmLabel, cancelLabel, pending, error, onConfirm, onCancel }: Props) {
  const ref = useRef<HTMLDialogElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const titleID = useId();
  const descriptionID = useId();

  // A modal dialog makes the rest of the page inert and manages focus and Tab navigation.
  useEffect(() => {
    const dialog = ref.current;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog?.showModal();
    if (variant !== "danger") { const input = dialog?.querySelector<HTMLInputElement>("input"); input?.focus(); input?.select(); }
    else cancelRef.current?.focus();
    return () => {
      dialog?.close();
      if (previousFocus?.isConnected) previousFocus.focus();
    };
  }, [variant]);

  const cancel = () => { if (!pending) onCancel(); };
  return <dialog ref={ref} className={`confirm-dialog ${variant}`} aria-modal="true" aria-labelledby={titleID} aria-describedby={descriptionID} aria-busy={pending} onCancel={(event) => { event.preventDefault(); cancel(); }}>
    <header className="confirm-header">
      <span className="confirm-icon" aria-hidden="true">{variant === "danger" ? <Trash2 size={23} strokeWidth={1.7} /> : variant === "rename" ? <Pencil size={23} strokeWidth={1.7} /> : <FolderPlus size={25} strokeWidth={1.7} />}</span>
      <button type="button" className="confirm-close" onClick={cancel} disabled={pending} aria-label={`关闭确认窗：${cancelLabel}`} title={cancelLabel}><X size={18} /></button>
      <div><span className="confirm-eyebrow">{variant === "create" ? "项目管理" : "会话管理"}</span><h2 id={titleID}>{title}</h2><p id={descriptionID}>{description}</p></div>
    </header>
    {children && <div className="confirm-body">{children}</div>}
    {error && <p className="confirm-error" role="alert"><CircleAlert size={16} /><span>{error}</span></p>}
    <footer className="confirm-actions">
      <button ref={cancelRef} type="button" className="confirm-cancel" onClick={cancel} disabled={pending}>{cancelLabel}</button>
      <button type={formID ? "submit" : "button"} form={formID} className="confirm-submit" onClick={formID ? undefined : () => { if (!pending) onConfirm(); }} disabled={pending}>
        {pending && <LoaderCircle className="spin" size={16} />}{pending ? "处理中…" : confirmLabel}
      </button>
    </footer>
  </dialog>;
}
