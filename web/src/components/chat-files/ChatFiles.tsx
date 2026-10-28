import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { ChevronDown, Download, Eye, Files, LoaderCircle, X } from "lucide-react";
import type { ChatFileDTO } from "../../types/api";
import { downloadChatFiles } from "../../api/history";
import { FilePreview } from "./FilePreview";
import { ImageThumbnail } from "./ImageThumbnail";

export function ChatFiles({ sessionID, chatID, files }: { sessionID: string; chatID: string; files: ChatFileDTO[] }) {
  const [pending, setPending] = useState<string>();
  const downloading = useRef(false);
  const [error, setError] = useState("");
  const [listOpen, setListOpen] = useState(false);
  const [position, setPosition] = useState<CSSProperties>();
  const [preview, setPreview] = useState<{ path: string; anchor: HTMLButtonElement; imageURL?: string }>();
  const trigger = useRef<HTMLButtonElement>(null);
  const card = useRef<HTMLElement>(null);
  const list = useRef<HTMLElement>(null);
  const openTimer = useRef<ReturnType<typeof setTimeout>>();
  const closeTimer = useRef<ReturnType<typeof setTimeout>>();
  const listPinned = useRef(false);
  const previewPinned = useRef(false);
  const listID = useId();
  const previewID = useId();
  const cancelTimers = useCallback(() => {
    clearTimeout(openTimer.current);
    clearTimeout(closeTimer.current);
  }, []);
  const closeAll = useCallback(() => {
    cancelTimers();
    listPinned.current = false;
    previewPinned.current = false;
    setPreview(undefined);
    setListOpen(false);
  }, [cancelTimers]);
  const closePreview = useCallback(() => {
    cancelTimers();
    previewPinned.current = false;
    setPreview(undefined);
  }, [cancelTimers]);
  useEffect(() => cancelTimers, [cancelTimers]);

  useEffect(() => {
    if (!listOpen && !preview) return;
    const outside = (event: Event) => {
      if (event.target instanceof Node && !card.current?.contains(event.target) && !list.current?.contains(event.target) && !document.getElementById(previewID)?.contains(event.target)) closeAll();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !preview) { trigger.current?.focus(); closeAll(); }
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      document.removeEventListener("keydown", escape);
    };
  }, [listOpen, preview, previewID, closeAll]);

  useLayoutEffect(() => {
    if (!listOpen) return;
    const update = () => {
      const rect = trigger.current?.getBoundingClientRect();
      if (!rect || rect.bottom < 0 || rect.top > window.innerHeight) { closeAll(); return; }
      const width = Math.min(440, window.innerWidth - 32);
      const above = rect.top - 24;
      const below = window.innerHeight - rect.bottom - 24;
      const height = Math.min(360, Math.max(above, below));
      setPosition({ width, maxHeight: height, left: Math.max(16, Math.min(rect.left, window.innerWidth - width - 16)),
        ...(above > below ? { bottom: window.innerHeight - rect.top + 8 } : { top: rect.bottom + 8 }) });
    };
    const scroll = (event: Event) => { if (!(event.target instanceof Node) || !list.current?.contains(event.target)) update(); };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", scroll, true);
    return () => { window.removeEventListener("resize", update); window.removeEventListener("scroll", scroll, true); };
  }, [listOpen, closeAll]);

  const scheduleClose = () => {
    cancelTimers();
    if (!listPinned.current && !previewPinned.current) closeTimer.current = setTimeout(closeAll, 300);
  };
  const showPreview = (path: string, anchor: HTMLButtonElement, imageURL?: string, pin = false) => {
    cancelTimers();
    if (pin) previewPinned.current = true;
    const show = () => setPreview({ path, anchor, imageURL });
    if (pin) show();
    else openTimer.current = setTimeout(show, 300);
  };
  const download = async (path?: string) => {
    if (downloading.current) return;
    downloading.current = true;
    setPending(path ?? "zip"); setError("");
    try {
      const { blob, filename } = await downloadChatFiles(sessionID, chatID, path);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url; link.download = filename;
      document.body.append(link); link.click(); link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "下载失败");
    } finally { downloading.current = false; setPending(undefined); }
  };
  const images = files.filter(file => /\.(png|jpe?g|gif|webp|bmp)$/i.test(file.path));
  const downloadButton = (path: string) => <button className="file-download" type="button" aria-label={`下载 /${path}`} title="下载文件" disabled={pending !== undefined} onClick={() => void download(path)}>{pending === path ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}</button>;

  return <section ref={card} className="chat-files" aria-label="本次任务新增或修改的文件">
    {images.length > 0 && <div className="chat-image-gallery">
      {images.map(file => <div className="chat-image-tile" key={file.path}>
        <ImageThumbnail sessionID={sessionID} chatID={chatID} path={file.path} onPreview={showPreview} onLeave={scheduleClose} />
        <div className="chat-image-caption"><span title={`/${file.path}`}>{file.path.split("/").pop()}</span>{downloadButton(file.path)}</div>
      </div>)}
    </div>}
    <div className="chat-files-summary">
      <button ref={trigger} type="button" className="chat-files-count" aria-expanded={listOpen} aria-controls={listOpen ? listID : undefined} aria-haspopup="dialog"
        onMouseEnter={() => { cancelTimers(); setListOpen(true); }} onMouseLeave={scheduleClose}
        onFocus={() => { cancelTimers(); setListOpen(true); }}
        onClick={() => { if (listPinned.current && listOpen) closeAll(); else { cancelTimers(); listPinned.current = true; setListOpen(true); } }}>
        <Files size={16} /><span>本次新增 / 修改 <strong>{files.length}</strong> 个文件</span><ChevronDown size={14} />
      </button>
      <button type="button" className="chat-files-zip" onClick={() => void download()} disabled={pending !== undefined}>{pending === "zip" ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}{pending === "zip" ? "正在打包…" : "下载 ZIP"}</button>
    </div>
    {error && <p className="chat-files-error" role="alert">{error}</p>}
    {listOpen && createPortal(<section ref={list} id={listID} role="dialog" aria-label="本次变更文件列表" className="chat-file-popover" style={position ?? { visibility: "hidden" }} onMouseEnter={cancelTimers} onMouseLeave={scheduleClose}>
      <header><div><strong>{files.length} 个变更文件</strong><small>悬停或点击文件名预览 · 当前版本</small></div><button type="button" aria-label="关闭文件列表" onClick={() => { trigger.current?.focus(); closeAll(); }}><X size={16} /></button></header>
      <ul>{files.map(file => <li key={file.path}>
        <button className="file-preview-trigger" type="button" aria-label={`预览 /${file.path}`} title={`/${file.path}`} aria-haspopup="dialog" aria-expanded={preview?.path === file.path} aria-controls={preview?.path === file.path ? previewID : undefined}
          onMouseEnter={event => showPreview(file.path, event.currentTarget)} onMouseLeave={() => clearTimeout(openTimer.current)}
          onFocus={event => showPreview(file.path, event.currentTarget, undefined, true)} onClick={event => showPreview(file.path, event.currentTarget, undefined, true)}>
          <Eye size={14} /><code>/{file.path}</code>
        </button>
        <span className={`file-change ${file.change_type}`}>{file.change_type === "created" ? "新增" : "修改"}</span>{downloadButton(file.path)}
      </li>)}</ul>
    </section>, document.body)}
    {preview && <FilePreview key={`${sessionID}:${chatID}:${preview.path}`} id={previewID} sessionID={sessionID} chatID={chatID} path={preview.path} anchor={preview.anchor} imageURL={preview.imageURL} onClose={closePreview} onMouseEnter={cancelTimers} onMouseLeave={scheduleClose} />}
  </section>;
}
