import { describe, expect, it } from "vitest";
import { modelRefs, splitModelRef } from "./models";
import type { ProviderListModelDTO } from "../types/api";

const payload: ProviderListModelDTO = {
  current_model: "b:model-2",
  providers: [
    { model_list: [{ model_name: "model-2", model_ref: "b:model-2" }] },
    { model_list: [{ model_name: "model-1", model_ref: "a:model-1" }, { model_name: "model-0", upstream_model: "up-0", model_ref: "a:model-0" }] },
  ],
};

describe("modelRefs", () => {
  it("flattens and sorts refs across providers", () => expect(modelRefs(payload)).toEqual(["a:model-0", "a:model-1", "b:model-2"]));
  it("returns empty list when catalog is empty", () => expect(modelRefs({ current_model: "", providers: [] })).toEqual([]));
});

describe("splitModelRef", () => {
  it("splits on the first colon", () => expect(splitModelRef("prov:mo:del")).toEqual({ provider: "prov", model: "mo:del" }));
  it("rejects malformed refs", () => { expect(splitModelRef("no-colon")).toBeNull(); expect(splitModelRef(":leading")).toBeNull(); expect(splitModelRef("trailing:")).toBeNull(); });
});
