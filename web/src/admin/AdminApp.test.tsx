import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { AdminApp } from "./AdminApp";

afterEach(() => vi.unstubAllGlobals());

describe("admin entry", () => {
  it("renders only the administrator login when there is no token", () => {
    vi.stubGlobal("sessionStorage", { getItem: () => null });
    const html = renderToStaticMarkup(<AdminApp />);
    expect(html).toContain("管理员账号");
    expect(html).toContain('type="password"');
    expect(html).not.toContain("删除");
    expect(html).not.toContain("请选择项目");
  });
  it("verifies a saved token before showing private data", () => {
    vi.stubGlobal("sessionStorage", { getItem: () => "expired-or-valid-token" });
    const html = renderToStaticMarkup(<AdminApp />);
    expect(html).toContain("正在验证登录");
    expect(html).not.toContain("历史消息");
  });
});
