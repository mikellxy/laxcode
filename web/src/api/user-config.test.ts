import { afterEach, expect, it, vi } from "vitest";
import { getUserConfig, updateUserConfig } from "./user-config";

vi.mock("../features/identity/guest", () => ({ getOrCreateGuestID: () => "test-user" }));
afterEach(() => vi.unstubAllGlobals());

it("reads config and acknowledges the version actually displayed", async () => {
  const fetch = vi.fn()
    .mockResolvedValueOnce(new Response(JSON.stringify({ user_guide_version: "v1", user_guide_seen_version: null })))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const config = await getUserConfig();
  await updateUserConfig({ user_guide_seen_version: config.user_guide_version });
  expect(fetch.mock.calls[0][0]).toBe("/api/user-config");
  expect(fetch.mock.calls[1][0]).toBe("/api/user-config");
  expect(fetch.mock.calls[1][1]).toMatchObject({ method: "PUT", headers: { "X-USERID": "test-user" }, body: JSON.stringify({ user_guide_seen_version: "v1" }) });
});

it.each([409, 500])("propagates save failure %s instead of marking the guide read", async (status) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "GUIDE_VERSION_CHANGED", message: "save failed" }), { status })));
  await expect(updateUserConfig({ user_guide_seen_version: "v1" })).rejects.toMatchObject({ status, message: "save failed" });
});
