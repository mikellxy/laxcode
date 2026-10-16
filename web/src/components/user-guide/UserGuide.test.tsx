import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { App } from "../../app/App";
import { UserGuide } from "./UserGuide";

afterEach(() => vi.unstubAllGlobals());

describe("new user guide", () => {
  it("shows the performance entrance followed by the result, with accessible exit and next actions", () => {
    const html = renderToStaticMarkup(<UserGuide onFinish={() => {}} />);
    expect(html).toContain("每一轮对话，耗时一目了然");
    expect(html.indexOf("performance_button.png")).toBeLessThan(html.indexOf("performance.png"));
    expect(html).toContain("下一步");
    expect(html).toContain("关闭引导，进入主页面");
    expect(html).toContain("跳过引导");
    expect(html).not.toContain("开始体验");
  });

  it("renders before the workspace even without backend/query setup, and does not mark itself seen on entry", () => {
    const document = { cookie: "laxcode_guest_id=existing-guest" };
    vi.stubGlobal("document", document);
    // Without a QueryClientProvider, mounting the workspace would throw.
    const html = renderToStaticMarkup(<App />);
    expect(html).toContain('aria-label="新用户引导"');
    expect(html).not.toContain("正在连接 LaxCode");
    expect(document.cookie).toBe("laxcode_guest_id=existing-guest");
  });
});
