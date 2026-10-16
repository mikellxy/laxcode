import { requestJSON } from "./client";
import type { AddModelInput, ModelDTO, ProviderListModelDTO, ProviderModelsDTO, SwitchModelDTO } from "../types/api";

export const listModels = () => requestJSON<ProviderListModelDTO>("/api/models");
export const switchModelRequest = (provider: string, model: string, reasoning_effort?: string) => requestJSON<SwitchModelDTO>("/api/model", { method: "POST", body: JSON.stringify({ provider, model, reasoning_effort }) });
export const addModelRequest = (input: AddModelInput) => requestJSON<ModelDTO>("/api/models", { method: "POST", body: JSON.stringify(input) });

export const modelProviders = (payload: ProviderListModelDTO): ProviderModelsDTO[] => payload.providers
  .filter((provider) => provider.model_list.length > 0)
  .map((provider) => ({ ...provider, model_list: [...provider.model_list].sort((a, b) => a.model_name.localeCompare(b.model_name)) }))
  .sort((a, b) => a.provider_name.localeCompare(b.provider_name));

export const splitModelRef = (ref: string): { provider: string; model: string } | null => {
  const index = ref.indexOf(":");
  if (index <= 0 || index === ref.length - 1) return null;
  return { provider: ref.slice(0, index), model: ref.slice(index + 1) };
};
