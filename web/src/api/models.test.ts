import { afterEach, describe, expect, it, vi } from "vitest";
import { modelProviders, splitModelRef, switchModelRequest } from "./models";
import type { ProviderListModelDTO } from "../types/api";

const payload: ProviderListModelDTO = {
  current_model: "b:model-2",
  providers: [
    { provider_name: "b", model_list: [{ model_name: "model-2", model_ref: "b:model-2" }] },
    { provider_name: "a", model_list: [{ model_name: "model-1", model_ref: "a:model-1" }, { model_name: "model-0", upstream_model: "up-0", model_ref: "a:model-0" }] },
  ],
};

afterEach(() => vi.unstubAllGlobals());

it("switches OAuth models and effort through the existing endpoint", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ model_ref: "openai:gpt-6.1-sol", reasoning_effort: "high" })));
  vi.stubGlobal("fetch", fetch);
  await switchModelRequest("openai", "gpt-6.1-sol", "high");
  expect(fetch.mock.calls[0][0]).toBe("/api/model");
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ provider: "openai", model: "gpt-6.1-sol", reasoning_effort: "high" });
});



describe("modelProviders", () => {
  it("sorts providers and their models without changing the catalog", () => {
    const providers = modelProviders(payload);
    expect(providers.map((provider) => provider.provider_name)).toEqual(["a", "b"]);
    expect(providers[0].model_list.map((model) => model.model_ref)).toEqual(["a:model-0", "a:model-1"]);
    expect(payload.providers[0].provider_name).toBe("b");
    expect(payload.providers[1].model_list[0].model_name).toBe("model-1");
  });
  it("omits providers without models", () => expect(modelProviders({ current_model: "", providers: [{ provider_name: "empty", model_list: [] }] })).toEqual([]));
  it("returns empty list when catalog is empty", () => expect(modelProviders({ current_model: "", providers: [] })).toEqual([]));
});

describe("splitModelRef", () => {
  it("splits on the first colon", () => expect(splitModelRef("prov:mo:del")).toEqual({ provider: "prov", model: "mo:del" }));
  it("rejects malformed refs", () => { expect(splitModelRef("no-colon")).toBeNull(); expect(splitModelRef(":leading")).toBeNull(); expect(splitModelRef("trailing:")).toBeNull(); });
});
