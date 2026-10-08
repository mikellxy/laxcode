import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { cancelChatGPTLogin, chatGPTLoginStatus, startChatGPTLogin } from "../../api/models";

export function ChatGPTLogin({ onConnected }: { onConnected: () => void }) {
  const [loginID, setLoginID] = useState("");
  const browser = useRef<Window | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; browser.current?.close(); };
  }, []);
  const start = useMutation({
    mutationFn: startChatGPTLogin,
    onSuccess: (result) => {
      if (!mounted.current) { void cancelChatGPTLogin(result.login_id).catch(() => undefined); return; }
      setLoginID(result.login_id);
      if (browser.current && result.authorization_url) browser.current.location.href = result.authorization_url;
    },
    onError: () => browser.current?.close(),
  });
  const status = useQuery({
    queryKey: ["chatgpt-login", loginID],
    queryFn: () => chatGPTLoginStatus(loginID),
    enabled: !!loginID,
    retry: false,
    refetchInterval: (query) => !query.state.error && (query.state.data?.status === "pending" || !query.state.data) ? 2000 : false,
  });
  useEffect(() => {
    if (status.data?.status === "connected") onConnected();
  }, [status.data?.status, onConnected]);
  useEffect(() => () => {
    if (loginID) void cancelChatGPTLogin(loginID).catch(() => undefined);
  }, [loginID]);
  const pending = start.isPending || (!!loginID && !status.isError && (!status.data || status.data.status === "pending"));
  const error = start.error ?? status.error;
  const begin = () => {
    // Open synchronously from the click so browser popup blockers allow login.
    browser.current = window.open("about:blank", "_blank");
    if (browser.current) browser.current.opener = null;
    start.mutate();
  };
  return <div className="chatgpt-login">
    <p>登录后将导入此账户可用的模型，使用已授权的 ChatGPT 订阅额度。</p>
    <button type="button" onClick={begin} disabled={pending}>{pending ? "等待 ChatGPT 授权…" : "Continue with ChatGPT"}</button>
    {pending && start.data?.authorization_url && <p><a href={start.data.authorization_url} target="_blank" rel="noreferrer">打开登录页面</a><span> · 登录将在 10 分钟后过期</span></p>}
    {(error || status.data?.error) && <p role="alert" className="model-form-error">{error instanceof Error ? error.message : status.data?.error}</p>}
  </div>;
}
