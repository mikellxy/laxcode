import { afterEach, describe, expect, it, vi } from "vitest";
import { pickDirectory } from "./directory-picker";

afterEach(() => vi.unstubAllGlobals());

describe("pickDirectory", () => {
  it("posts to the host picker endpoint and returns its absolute path", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ path: "/Users/test/project/" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(pickDirectory()).resolves.toEqual({ path: "/Users/test/project/" });
    expect(fetchMock).toHaveBeenCalledWith("/api/directory-picker", expect.objectContaining({
      method: "POST",
      body: "{}",
      headers: expect.objectContaining({ "Content-Type": "application/json" }),
    }));
  });

  it("returns null when the operator cancels the system picker", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ path: null }), { status: 200 })));
    await expect(pickDirectory()).resolves.toEqual({ path: null });
  });
});
