import { requestJSON } from "./client";
import type { AddModelInput, ModelDTO, ProviderListModelDTO, SwitchModelDTO } from "../types/api";

export const listModels = () => requestJSON<ProviderListModelDTO>("/api/models");
export const switchModelRequest = (provider: string, model: string) => requestJSON<SwitchModelDTO>("/api/model", { method: "POST", body: JSON.stringify({ provider, model }) });
export const addModelRequest = (input: AddModelInput) => requestJSON<ModelDTO>("/api/models", { method: "POST", body: JSON.stringify(input) });

export const modelRefs = (payload: ProviderListModelDTO): string[] => payload.providers.flatMap((provider) => provider.model_list.map((model) => model.model_ref)).sort();

export const splitModelRef = (ref: string): { provider: string; model: string } | null => {
  const index = ref.indexOf(":");
  if (index <= 0 || index === ref.length - 1) return null;
  return { provider: ref.slice(0, index), model: ref.slice(index + 1) };
};
