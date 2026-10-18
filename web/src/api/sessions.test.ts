import { afterEach, expect, it, vi } from "vitest";
import { ApiError } from "./client";
import { deleteSession } from "./sessions";

afterEach(() => vi.unstubAllGlobals());

it("deletes an owned session and accepts an empty 204 response", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  await expect(deleteSession("user", "session")).resolves.toBeUndefined();
  expect(fetch).toHaveBeenCalledWith("/api/sessions/session?user_id=user", expect.objectContaining({ method: "DELETE" }));
});

it("keeps deletion failures available to the UI", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "会话正在执行" }), { status: 409 })));
  await expect(deleteSession("user", "session")).rejects.toMatchObject({ message: "会话正在执行", status: 409 });
  await expect(deleteSession("user", "session")).rejects.toBeInstanceOf(ApiError);
});

vi.mock("../features/identity/guest", () => ({ getOrCreateGuestID: () => "11111111-1111-4111-8111-111111111111" }));
