import { useEffect, useState, type FormEvent } from "react";
import type { AddModelInput } from "../../types/api";
import { ChatGPTLogin } from "./ChatGPTLogin";

type Props = {
  saving: boolean;
  error?: string;
  onCancel: () => void;
  onSubmit: (input: AddModelInput) => void;
  onConnected: () => void;
};

const initialForm: AddModelInput = {
  provider: "",
  model: "",
  api_key: "",
  base_url: "",
  context_window: 200000,
  max_output_tokens: 16384,
};

export function AddModelDialog({ saving, error, onCancel, onSubmit, onConnected }: Props) {
  const [authType, setAuthType] = useState("api_key");
  const [form, setForm] = useState(initialForm);
  const [validationError, setValidationError] = useState("");
  const update = <K extends keyof AddModelInput>(key: K, value: AddModelInput[K]) =>
    setForm((current) => ({ ...current, [key]: value }));
  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === "Escape" && !saving) onCancel(); };
    document.addEventListener("keydown", close);
    return () => document.removeEventListener("keydown", close);
  }, [saving, onCancel]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (authType !== "api_key") return;
    if (form.max_output_tokens >= form.context_window) {
      setValidationError("输出 Token 必须小于上下文大小");
      return;
    }
    setValidationError("");
    onSubmit({
      ...form,
      provider: form.provider.trim(),
      model: form.model.trim(),
      api_key: form.api_key.trim(),
      base_url: form.base_url.trim(),
    });
  };

  return <div className="model-dialog-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !saving) onCancel(); }}>
    <form className="model-dialog" role="dialog" aria-modal="true" aria-label="添加模型" onSubmit={submit}>
      <header><h2>添加模型</h2><p>选择模型接入方式</p></header>
      <label className="auth-method"><span>认证方式</span><select value={authType} onChange={(event) => setAuthType(event.target.value)} disabled={saving}><option value="api_key">API Key</option><option value="oauth">ChatGPT 订阅</option></select></label>
      {authType === "oauth" ? <ChatGPTLogin onConnected={onConnected} /> : <div className="model-form-grid">
        <label><span>Provider</span><input autoFocus required value={form.provider} onChange={(event) => update("provider", event.target.value)} placeholder="例如 openai" /></label>
        <label><span>Model</span><input required value={form.model} onChange={(event) => update("model", event.target.value)} placeholder="例如 gpt-5" /></label>
        <label className="full"><span>Base URL</span><input required type="url" value={form.base_url} onChange={(event) => update("base_url", event.target.value)} placeholder="https://api.example.com/v1" /></label>
        <label className="full"><span>API Key</span><input required type="password" autoComplete="new-password" value={form.api_key} onChange={(event) => update("api_key", event.target.value)} placeholder="sk-..." /></label>
        <label><span>上下文大小</span><input required type="number" min={2} value={form.context_window} onChange={(event) => update("context_window", Number(event.target.value))} /></label>
        <label><span>输出 Token 大小</span><input required type="number" min={1} value={form.max_output_tokens} onChange={(event) => update("max_output_tokens", Number(event.target.value))} /></label>
      </div>}
      {(validationError || error) && <p className="model-form-error">{validationError || error}</p>}
      <div className="model-dialog-actions"><button type="button" onClick={onCancel} disabled={saving}>取消</button>{authType === "api_key" && <button type="submit" disabled={saving}>{saving ? "保存中…" : "添加模型"}</button>}</div>
    </form>
  </div>;
}
