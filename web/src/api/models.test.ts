import { afterEach, describe, expect, it, vi } from "vitest";
import { cancelChatGPTLogin, chatGPTLoginStatus, modelRefs, splitModelRef, startChatGPTLogin, switchModelRequest } from "./models";
import type { ProviderListModelDTO } from "../types/api";

const payload: ProviderListModelDTO = {
  current_model: "b:model-2",
  providers: [
    { model_list: [{ model_name: "model-2", model_ref: "b:model-2" }] },
    { model_list: [{ model_name: "model-1", model_ref: "a:model-1" }, { model_name: "model-0", upstream_model: "up-0", model_ref: "a:model-0" }] },
  ],
};

afterEach(() => vi.unstubAllGlobals());

it("switches OAuth models and effort through the existing endpoint", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ model_ref: "openai-chatgpt:gpt-6.1-sol", reasoning_effort: "high" })));
  vi.stubGlobal("fetch", fetch);
  await switchModelRequest("openai-chatgpt", "gpt-6.1-sol", "high");
  expect(fetch.mock.calls[0][0]).toBe("/api/model");
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ provider: "openai-chatgpt", model: "gpt-6.1-sol", reasoning_effort: "high" });
});

it("starts, polls and cancels login without requiring a token in the browser", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ login_id: "login", status: "pending" }))).mockResolvedValueOnce(new Response(JSON.stringify({ login_id: "login", status: "connected" }))).mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  await startChatGPTLogin();
  await chatGPTLoginStatus("login");
  await cancelChatGPTLogin("login");
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/auth/chatgpt", "/api/auth/chatgpt/login", "/api/auth/chatgpt/login"]);
  expect(fetch.mock.calls[2][1].method).toBe("DELETE");
});

describe("modelRefs", () => {
  it("flattens and sorts refs across providers", () => expect(modelRefs(payload)).toEqual(["a:model-0", "a:model-1", "b:model-2"]));
  it("returns empty list when catalog is empty", () => expect(modelRefs({ current_model: "", providers: [] })).toEqual([]));
});

describe("splitModelRef", () => {
  it("splits on the first colon", () => expect(splitModelRef("prov:mo:del")).toEqual({ provider: "prov", model: "mo:del" }));
  it("rejects malformed refs", () => { expect(splitModelRef("no-colon")).toBeNull(); expect(splitModelRef(":leading")).toBeNull(); expect(splitModelRef("trailing:")).toBeNull(); });
});
