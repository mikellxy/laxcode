import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Download, Eye, Files, LoaderCircle } from "lucide-react";
import type { ChatFileDTO } from "../../types/api";
import { downloadChatFiles } from "../../api/history";
import { FilePreview } from "./FilePreview";

export function ChatFiles({ sessionID, chatID, files }: { sessionID: string; chatID: string; files: ChatFileDTO[] }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<{ path: string; anchor: HTMLButtonElement }>();
  const timer = useRef<ReturnType<typeof setTimeout>>();
  const pinned = useRef(false);
  const previewID = useId();
  const cancelTimer = () => clearTimeout(timer.current);
  const closePreview = useCallback(() => {
    clearTimeout(timer.current);
    pinned.current = false;
    setPreview(undefined);
  }, []);
  useEffect(() => () => clearTimeout(timer.current), []);
  const showPreview = (path: string, anchor: HTMLButtonElement, pin = false) => {
    cancelTimer();
    pinned.current = pin;
    setPreview({ path, anchor });
  };
  const scheduleClose = () => {
    cancelTimer();
    if (!pinned.current) timer.current = setTimeout(closePreview, 200);
  };
  const download = async () => {
    if (pending) return;
    setPending(true); setError("");
    try {
      const blob = await downloadChatFiles(sessionID, chatID);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url; link.download = "chat-files.zip";
      document.body.append(link); link.click(); link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "下载失败");
    } finally { setPending(false); }
  };
  return <section className="chat-files" aria-label="本次任务新增或修改的文件">
    <header><span><Files size={16} />本次任务新增 / 修改的文件</span><small>{files.length} 个</small></header>
    <ul>{files.map((file) => <li key={file.path}><button
      className="file-preview-trigger" type="button" aria-label={`预览 /${file.path}`}
      aria-haspopup="dialog" aria-expanded={preview?.path === file.path}
      aria-controls={preview?.path === file.path ? previewID : undefined}
      onMouseEnter={event => {
        cancelTimer();
        if (preview?.path !== file.path) {
          const anchor = event.currentTarget;
          timer.current = setTimeout(() => showPreview(file.path, anchor), 300);
        }
      }}
      onMouseLeave={scheduleClose}
      onFocus={event => showPreview(file.path, event.currentTarget)}
      onClick={event => showPreview(file.path, event.currentTarget, true)}
    ><code>/{file.path}</code><Eye size={14} aria-hidden="true" /></button><span className={`file-change ${file.change_type}`}>{file.change_type === "created" ? "新增" : "修改"}</span></li>)}</ul>
    <footer><small>下载文件当前版本</small><button type="button" onClick={() => void download()} disabled={pending}>{pending ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}{pending ? "正在打包…" : "打包下载 ZIP"}</button></footer>
    {error && <p className="chat-files-error" role="alert">{error}</p>}
    {preview && <FilePreview key={`${sessionID}:${chatID}:${preview.path}`} id={previewID} sessionID={sessionID} chatID={chatID} path={preview.path} anchor={preview.anchor} onClose={closePreview} onMouseEnter={cancelTimer} onMouseLeave={scheduleClose} />}
  </section>;
}
