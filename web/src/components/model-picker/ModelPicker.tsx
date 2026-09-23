import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronUp, Clock, Loader2, Plus } from "lucide-react";
import { addModelRequest, listModels, modelRefs, splitModelRef, switchModelRequest } from "../../api/models";
import type { AddModelInput, ProviderListModelDTO } from "../../types/api";
import { AddModelDialog } from "./AddModelDialog";

export function ModelPicker({ running }: { running: boolean }) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [adding, setAdding] = useState(false);
  const [queued, setQueued] = useState<string | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const models = useQuery({ queryKey: ["models"], queryFn: listModels });
  const switchModel = useMutation({
    mutationFn: async (ref: string) => {
      const parts = splitModelRef(ref);
      if (!parts) throw new Error(`无效的模型引用：${ref}`);
      return switchModelRequest(parts.provider, parts.model);
    },
    onSuccess: ({ model_ref }) => {
      client.setQueryData(["models"], (old: ProviderListModelDTO | undefined) => (old ? { ...old, current_model: model_ref } : old));
      void client.invalidateQueries({ queryKey: ["context"] });
      setOpen(false);
    },
  });
  const addModel = useMutation({
    mutationFn: (input: AddModelInput) => addModelRequest(input),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["models"] });
      setAdding(false);
      setOpen(true);
    },
  });

  // SSE 流式期间不真正发切换请求（后端约定切换只在无进行中 Chat 时原子生效）：
  // 点击仅排队，流结束（running 由 true 变 false）后再发送排队的引用。
  useEffect(() => {
    if (running || !queued) return;
    const ref = queued;
    setQueued(null);
    switchModel.mutate(ref);
  }, [running, queued, switchModel]);

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
    const cancel = (event: KeyboardEvent) => { if (event.key === "Escape") setOpen(false); };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", cancel);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", cancel); };
  }, [open]);

  const current = models.data?.current_model;
  const refs = models.data ? modelRefs(models.data) : [];
  const pick = (ref: string) => {
    if (ref === current || switchModel.isPending) return;
    if (running) { setQueued(ref); setOpen(false); return; }
    switchModel.mutate(ref);
  };
  return <div className="model-picker" ref={root}>
    <button className={`model-trigger ${queued ? "queued" : ""}`} onClick={() => setOpen((value) => !value)} disabled={models.isPending} aria-haspopup="listbox" aria-expanded={open}>
      {models.isError ? "模型列表不可用" : queued ?? current ?? "加载模型…"}
      {switchModel.isPending ? <Loader2 size={13} className="spin" /> : queued ? <Clock size={13} /> : open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
    </button>
    {open && <ul className="model-menu" role="listbox">
      {models.isError && <li className="model-menu-state" onClick={() => models.refetch()}>加载失败，点击重试</li>}
      {!models.isError && refs.map((ref) => <li key={ref} role="option" aria-selected={ref === current} className={`model-item ${ref === current ? "active" : ""} ${ref === queued ? "queued" : ""}`} onClick={() => pick(ref)}>
        <span>{ref}{ref === queued ? " · 待生效" : ""}</span>
        {ref === current ? <Check size={14} /> : ref === queued ? <Clock size={14} /> : null}
      </li>)}
      {switchModel.isError && <li className="model-menu-state error">切换失败：{switchModel.error instanceof Error ? switchModel.error.message : "未知错误"}</li>}
      <li className="model-add-item"><button type="button" onClick={() => { addModel.reset(); setOpen(false); setAdding(true); }}><Plus size={14} />添加模型</button></li>
    </ul>}
    {adding && <AddModelDialog saving={addModel.isPending} error={addModel.isError ? (addModel.error instanceof Error ? addModel.error.message : "保存失败") : undefined} onCancel={() => { addModel.reset(); setAdding(false); }} onSubmit={(input) => addModel.mutate(input)} />}
  </div>;
}
