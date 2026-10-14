import { useState } from "react";
import { Download, Files, LoaderCircle } from "lucide-react";
import type { ChatFileDTO } from "../../types/api";
import { downloadChatFiles } from "../../api/history";

export function ChatFiles({ sessionID, chatID, files }: { sessionID: string; chatID: string; files: ChatFileDTO[] }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
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
    <ul>{files.map((file) => <li key={file.path}><code>/{file.path}</code><span className={`file-change ${file.change_type}`}>{file.change_type === "created" ? "新增" : "修改"}</span></li>)}</ul>
    <footer><small>下载文件当前版本</small><button type="button" onClick={() => void download()} disabled={pending}>{pending ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}{pending ? "正在打包…" : "打包下载 ZIP"}</button></footer>
    {error && <p className="chat-files-error" role="alert">{error}</p>}
  </section>;
}
