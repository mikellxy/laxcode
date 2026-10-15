import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { CircleAlert, FileText, Image, LoaderCircle, X } from "lucide-react";
import { readChatFile, type ChatFilePreview } from "../../api/history";

type Props = {
  id: string;
  sessionID: string;
  chatID: string;
  path: string;
  anchor: HTMLButtonElement;
  onClose: () => void;
  onMouseEnter: () => void;
  onMouseLeave: () => void;
};
type Preview = Exclude<ChatFilePreview, { kind: "image" }> | { kind: "image"; url: string };

export function FilePreview({ id, sessionID, chatID, path, anchor, onClose, onMouseEnter, onMouseLeave }: Props) {
  const panel = useRef<HTMLElement>(null);
  const [position, setPosition] = useState<CSSProperties>();
  const [preview, setPreview] = useState<Preview>();
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    let imageURL: string | undefined;
    void readChatFile(sessionID, chatID, path, controller.signal).then(result => {
      if (controller.signal.aborted) return;
      if (result.kind === "image") {
        imageURL = URL.createObjectURL(result.blob);
        setPreview({ kind: "image", url: imageURL });
      } else setPreview(result);
    }).catch(cause => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "文件预览失败");
    });
    return () => {
      controller.abort();
      if (imageURL) URL.revokeObjectURL(imageURL);
    };
  }, [sessionID, chatID, path]);

  useLayoutEffect(() => {
    const updatePosition = () => {
      const rect = anchor.getBoundingClientRect();
      if (!anchor.isConnected || rect.bottom < 0 || rect.top > window.innerHeight) { onClose(); return; }
      const width = Math.min(640, window.innerWidth - 32);
      const above = rect.top - 26;
      const below = window.innerHeight - rect.bottom - 26;
      const height = Math.min(460, window.innerHeight - 32, Math.max(180, above, below));
      const top = above > below ? rect.top - height - 10 : rect.bottom + 10;
      setPosition({ width, height, left: Math.max(16, Math.min(rect.left, window.innerWidth - width - 16)), top: Math.max(16, top) });
    };
    const scroll = (event: Event) => {
      if (!(event.target instanceof Node) || !panel.current?.contains(event.target)) updatePosition();
    };
    updatePosition();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", scroll, true);
    return () => { window.removeEventListener("resize", updatePosition); window.removeEventListener("scroll", scroll, true); };
  }, [anchor, onClose]);

  useEffect(() => {
    const outside = (event: Event) => {
      if (event.target instanceof Node && !anchor.contains(event.target) && !panel.current?.contains(event.target)) onClose();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      if (panel.current?.contains(document.activeElement)) anchor.focus();
      onClose();
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside, true);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside, true);
      document.removeEventListener("keydown", escape);
    };
  }, [anchor, onClose]);

  return createPortal(<section
    ref={panel} id={id} role="dialog" aria-modal="false" aria-label={`文件预览 /${path}`} className="file-preview"
    style={position ?? { visibility: "hidden" }} onMouseEnter={onMouseEnter} onMouseLeave={onMouseLeave}
  >
    <header className="file-preview-header">
      <span className="file-preview-icon">{preview?.kind === "image" ? <Image size={20} /> : <FileText size={20} />}</span>
      <div><small>{preview?.kind === "image" ? "图片预览" : "文件预览"}</small><strong title={`/${path}`}>/{path}</strong></div>
      <button type="button" aria-label="关闭文件预览" onClick={() => { anchor.focus(); onClose(); }}><X size={18} /></button>
    </header>
    <div className={`file-preview-body ${preview?.kind === "image" ? "image" : ""}`}>
      {error ? <div className="file-preview-state" role="alert"><CircleAlert size={24} /><strong>暂时无法预览</strong><span>{error}</span></div>
        : !preview ? <div className="file-preview-state" role="status"><LoaderCircle size={24} className="spin" /><span>正在读取文件…</span></div>
        : preview.kind === "image" ? <img src={preview.url} alt={`/${path}`} onError={() => setError("图片无法解码，请下载后查看")} />
        : <pre tabIndex={0} aria-label="文件内容（只读）">{preview.content || "（空文件）"}</pre>}
    </div>
    <footer className="file-preview-footer"><span className="file-preview-readonly">只读</span><span>{preview?.kind === "text" && preview.truncated ? "仅展示前 256 KiB，完整内容请下载 ZIP" : "文件当前版本"}</span></footer>
  </section>, document.body);
}
