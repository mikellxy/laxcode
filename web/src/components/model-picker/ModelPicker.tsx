import { useEffect, useId, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronRight, ChevronUp, Clock, Loader2 } from "lucide-react";
import { listModels, modelProviders, splitModelRef, switchModelRequest } from "../../api/models";
import type { ProviderListModelDTO } from "../../types/api";

export function ModelPicker({ running }: { running: boolean }) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [expandedProvider, setExpandedProvider] = useState<string | null>(null);
  const menuID = useId();
  const [queued, setQueued] = useState<string | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const models = useQuery({ queryKey: ["models"], queryFn: listModels });
  const switchModel = useMutation({
    mutationFn: async ({ ref, effort }: { ref: string; effort?: string }) => {
      const parts = splitModelRef(ref);
      if (!parts) throw new Error(`无效的模型引用：${ref}`);
      return switchModelRequest(parts.provider, parts.model, effort);
    },
    onSuccess: ({ model_ref, reasoning_effort }) => {
      client.setQueryData(["models"], (old: ProviderListModelDTO | undefined) => (old ? { ...old, current_model: model_ref, current_reasoning_effort: reasoning_effort } : old));
      void client.invalidateQueries({ queryKey: ["context"] });
      setOpen(false);
    },
  });
  // SSE 流式期间不真正发切换请求（后端约定切换只在无进行中 Chat 时原子生效）：
  // 点击仅排队，流结束（running 由 true 变 false）后再发送排队的引用。
  useEffect(() => {
    if (running || !queued) return;
    const ref = queued;
    setQueued(null);
    switchModel.mutate({ ref });
  }, [running, queued, switchModel]);

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
    const cancel = (event: KeyboardEvent) => { if (event.key === "Escape") { setOpen(false); root.current?.querySelector<HTMLButtonElement>(".model-trigger")?.focus(); } };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", cancel);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", cancel); };
  }, [open]);

  const current = models.data?.current_model;
  const selected = models.data?.providers.flatMap((provider) => provider.model_list).find((model) => model.model_ref === current);
  const providers = models.data ? modelProviders(models.data) : [];
  const expanded = providers.find((provider) => provider.provider_name === expandedProvider);
  const label = models.isError ? "模型列表不可用" : queued ?? (models.isPending ? "加载模型…" : current || "未配置模型");
  const pick = (ref: string) => {
    if (ref === current || switchModel.isPending) return;
    if (running) { setQueued(ref); setOpen(false); return; }
    switchModel.mutate({ ref });
  };
  return <div className="model-picker" ref={root}>
    <button className={`model-trigger ${queued ? "queued" : ""}`} onClick={() => { setExpandedProvider(null); setOpen((value) => !value); }} disabled={models.isPending} aria-controls={open ? menuID : undefined} aria-expanded={open}>
      {label}
      {switchModel.isPending ? <Loader2 size={13} className="spin" /> : queued ? <Clock size={13} /> : open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
    </button>
    {!!selected?.reasoning_efforts?.length && <select className="reasoning-select" aria-label="思考强度" value={models.data?.current_reasoning_effort || selected.reasoning_effort || ""} disabled={running || switchModel.isPending} onChange={(event) => { if (current) switchModel.mutate({ ref: current, effort: event.target.value }); }}><option value="">模型默认</option>{selected.reasoning_efforts.map((effort) => <option key={effort} value={effort}>{effort}</option>)}</select>}
    {open && <div id={menuID} className="model-menu" role="group" aria-label="选择模型">
      {models.isError && <button type="button" className="model-menu-state" onClick={() => models.refetch()}>加载失败，点击重试</button>}
      {!models.isError && providers.length === 0 && <div className="model-menu-state">暂无可用模型，请联系管理员</div>}
      {!models.isError && <div className="model-menu-columns">
        <ul className="model-provider-list" aria-label="提供商">
          {providers.map((provider) => <li key={provider.provider_name}>
            <button type="button" className={`model-item ${provider.provider_name === expandedProvider ? "expanded" : ""} ${provider.model_list.some((model) => model.model_ref === current) ? "active" : ""}`}
              aria-expanded={provider.provider_name === expandedProvider} aria-controls={provider.provider_name === expandedProvider ? `${menuID}-models` : undefined}
              onClick={() => setExpandedProvider(provider.provider_name === expandedProvider ? null : provider.provider_name)}
              onKeyDown={(event) => {
                if (event.key !== "ArrowRight") return;
                event.preventDefault();
                setExpandedProvider(provider.provider_name);
                window.requestAnimationFrame(() => root.current?.querySelector<HTMLButtonElement>(".model-submenu button")?.focus());
              }}>
              <span>{provider.provider_name}</span><ChevronRight size={14} />
            </button>
          </li>)}
        </ul>
        {expanded && <ul id={`${menuID}-models`} className="model-submenu" aria-label={`${expanded.provider_name} 的模型`} onKeyDown={(event) => {
          if (event.key !== "ArrowLeft") return;
          event.preventDefault();
          root.current?.querySelector<HTMLButtonElement>(".model-provider-list button[aria-expanded='true']")?.focus();
          setExpandedProvider(null);
        }}>
          {expanded.model_list.map((model) => <li key={model.model_ref}>
            <button type="button" aria-pressed={model.model_ref === current} disabled={switchModel.isPending}
              className={`model-item ${model.model_ref === current ? "active" : ""} ${model.model_ref === queued ? "queued" : ""}`} onClick={() => pick(model.model_ref)}>
              <span>{model.model_name}{model.model_ref === queued ? " · 待生效" : ""}</span>
              {model.model_ref === current ? <Check size={14} /> : model.model_ref === queued ? <Clock size={14} /> : null}
            </button>
          </li>)}
        </ul>}
      </div>}
      {switchModel.isError && <div className="model-menu-state error" role="alert">切换失败：{switchModel.error instanceof Error ? switchModel.error.message : "未知错误"}</div>}
    </div>}
  </div>;
}
