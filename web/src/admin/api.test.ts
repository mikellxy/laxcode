import { afterEach, describe, expect, it, vi } from "vitest";
import { adminRequest, AdminAPIError, isAdminPath } from "./api";

afterEach(() => vi.unstubAllGlobals());

describe("admin API isolation", () => {
  it("uses only the admin token and never guest identity or cookies", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ users: [] })));
    vi.stubGlobal("fetch", fetch);
    await adminRequest("/users", "signed-token");
    const [url, init] = fetch.mock.calls[0];
    expect(url).toBe("/admin/api/users");
    expect(init.headers.get("Authorization")).toBe("Bearer signed-token");
    expect(init.headers.has("X-USERID")).toBe(false);
    expect(init.credentials).toBe("omit");
    expect(init.cache).toBe("no-store");
  });
  it("logs in without a bearer token and exposes authentication failures", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "登录已失效" }), { status: 401 }));
    vi.stubGlobal("fetch", fetch);
    await expect(adminRequest("/login", undefined, { method: "POST", body: "{}" })).rejects.toMatchObject({ status: 401, message: "登录已失效" });
    expect(fetch.mock.calls[0][1].headers.has("Authorization")).toBe(false);
    expect(fetch.mock.calls[0][1].headers.get("Content-Type")).toBe("application/json");
  });
  it("handles non-JSON errors without treating them as successful login", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("unavailable", { status: 503 })));
    await expect(adminRequest("/me", "token")).rejects.toBeInstanceOf(AdminAPIError);
  });
  it("routes only the admin page prefix to the admin app", () => {
    expect(["/admin", "/admin/", "/admin/login"].every(isAdminPath)).toBe(true);
    expect(["/", "/administrator", "/api/projects"].some(isAdminPath)).toBe(false);
  });
});
