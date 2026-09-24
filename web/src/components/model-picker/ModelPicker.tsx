import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronUp, Clock, Loader2, Settings } from "lucide-react";
import { addModelRequest, listModels, modelRefs, splitModelRef, switchModelRequest } from "../../api/models";
import type { AddModelInput, ProviderListModelDTO } from "../../types/api";
import { AddModelDialog } from "./AddModelDialog";

// ModelPicker 承载模型选择触发器与旁侧的「添加模型」齿轮按钮：齿轮直接打开
// 添加表单；bounceKey 由父组件在用户点击被锁定的输入框时递增，驱动齿轮跳动
// 引导配置模型。
export function ModelPicker({ running, bounceKey }: { running: boolean; bounceKey: number }) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [adding, setAdding] = useState(false);
  const [queued, setQueued] = useState<string | null>(null);
  const [bouncing, setBouncing] = useState(false);
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

  // 父组件递增 bounceKey（用户点击了锁定中的输入框）时让齿轮跳动一次。
  useEffect(() => {
    if (!bounceKey) return;
    // 先移除再于下一帧恢复 class，使上一轮尚未结束时再次点击也能从头播放。
    setBouncing(false);
    let timer: number | undefined;
    const frame = window.requestAnimationFrame(() => {
      setBouncing(true);
      timer = window.setTimeout(() => setBouncing(false), 700);
    });
    return () => {
      window.cancelAnimationFrame(frame);
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [bounceKey]);

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
  const label = models.isError ? "模型列表不可用" : queued ?? (models.isPending ? "加载模型…" : current || "未配置模型");
  const pick = (ref: string) => {
    if (ref === current || switchModel.isPending) return;
    if (running) { setQueued(ref); setOpen(false); return; }
    switchModel.mutate(ref);
  };
  return <div className="model-picker" ref={root}>
    <button className={`model-trigger ${queued ? "queued" : ""}`} onClick={() => setOpen((value) => !value)} disabled={models.isPending} aria-haspopup="listbox" aria-expanded={open}>
      {label}
      {switchModel.isPending ? <Loader2 size={13} className="spin" /> : queued ? <Clock size={13} /> : open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
    </button>
    <button type="button" className={`model-gear ${bouncing ? "bounce" : ""}`} onClick={() => { addModel.reset(); setOpen(false); setAdding(true); }} aria-label="添加模型" aria-haspopup="dialog" title="添加模型"><Settings size={14} /></button>
    {open && <ul className="model-menu" role="listbox">
      {models.isError && <li className="model-menu-state" onClick={() => models.refetch()}>加载失败，点击重试</li>}
      {!models.isError && refs.length === 0 && <li className="model-menu-state">还没有模型，点击齿轮按钮添加</li>}
      {!models.isError && refs.map((ref) => <li key={ref} role="option" aria-selected={ref === current} className={`model-item ${ref === current ? "active" : ""} ${ref === queued ? "queued" : ""}`} onClick={() => pick(ref)}>
        <span>{ref}{ref === queued ? " · 待生效" : ""}</span>
        {ref === current ? <Check size={14} /> : ref === queued ? <Clock size={14} /> : null}
      </li>)}
      {switchModel.isError && <li className="model-menu-state error">切换失败：{switchModel.error instanceof Error ? switchModel.error.message : "未知错误"}</li>}
    </ul>}
    {adding && <AddModelDialog saving={addModel.isPending} error={addModel.isError ? (addModel.error instanceof Error ? addModel.error.message : "保存失败") : undefined} onCancel={() => { addModel.reset(); setAdding(false); }} onSubmit={(input) => addModel.mutate(input)} />}
  </div>;
}
