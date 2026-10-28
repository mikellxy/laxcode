import { useEffect, useRef, useState } from "react";
import { Image, LoaderCircle } from "lucide-react";
import { readChatFile } from "../../api/history";

type Props = {
  sessionID: string;
  chatID: string;
  path: string;
  onPreview: (path: string, anchor: HTMLButtonElement, imageURL?: string, pin?: boolean) => void;
  onLeave: () => void;
};

export function ImageThumbnail({ sessionID, chatID, path, onPreview, onLeave }: Props) {
  const button = useRef<HTMLButtonElement>(null);
  const [visible, setVisible] = useState(false);
  const [url, setURL] = useState<string>();
  const [error, setError] = useState(false);
  useEffect(() => {
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) { setVisible(true); observer.disconnect(); }
    });
    if (button.current) observer.observe(button.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let objectURL: string | undefined;
    // shortcut: fetch the original only when visible; add server thumbnails if image volume grows.
    void readChatFile(sessionID, chatID, path, controller.signal).then(result => {
      if (controller.signal.aborted) return;
      if (result.kind !== "image") { setError(true); return; }
      objectURL = URL.createObjectURL(result.blob);
      setURL(objectURL);
    }).catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => { controller.abort(); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [visible, sessionID, chatID, path]);
  return <button ref={button} type="button" className="chat-image-thumbnail" aria-label={`预览 /${path}`} aria-haspopup="dialog"
    onMouseEnter={event => onPreview(path, event.currentTarget, url)} onMouseLeave={onLeave}
    onFocus={event => onPreview(path, event.currentTarget, url, true)} onClick={event => onPreview(path, event.currentTarget, url, true)}>
    {error ? <span><Image size={22} />预览不可用</span> : url ? <img src={url} alt={path.split("/").pop()} onError={() => setError(true)} /> : <LoaderCircle size={20} className={visible ? "spin" : ""} />}
  </button>;
}
