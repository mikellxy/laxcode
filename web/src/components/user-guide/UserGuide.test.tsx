import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { App } from "../../app/App";
import { UserGuide } from "./UserGuide";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("react", async (original) => ({ ...await original<typeof import("react")>(), useLayoutEffect: vi.fn() }));

afterEach(() => vi.unstubAllGlobals());

describe("new user guide", () => {
  it("shows the coding examples in order, with accessible exit and next actions", () => {
    const html = renderToStaticMarkup(<UserGuide onFinish={() => {}} />);
    expect(html).toContain("随时随地发指令，云端写代码");
    expect(html.indexOf("yd_code_1.png")).toBeLessThan(html.indexOf("yd_code_2.png"));
    expect(html.indexOf("yd_code_2.png")).toBeLessThan(html.indexOf("yd_code_3.png"));
    expect(html).toContain("下一步");
    expect(html).toContain("关闭引导，进入主页面");
    expect(html).toContain("跳过引导");
    expect(html).not.toContain("开始体验");
  });

  it.each([null, "old-version"])("uses server state instead of the legacy seen cookie: %s", (seen) => {
    const document = { cookie: "laxcode_guest_id=existing-guest; laxcode_guide_seen=1" };
    vi.stubGlobal("document", document);
    const client = new QueryClient();
    client.setQueryData(["user-config", "existing-guest"], { user_guide_version: "new-version", user_guide_seen_version: seen });
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><App /></QueryClientProvider>);
    expect(html).toContain('aria-label="新用户引导"');
    expect(document.cookie).toBe("laxcode_guest_id=existing-guest; laxcode_guide_seen=1");
    client.clear();
  });

  it("waits for user config before showing either guide or workspace", () => {
    vi.stubGlobal("document", { cookie: "laxcode_guest_id=existing-guest" });
    const client = new QueryClient();
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><App /></QueryClientProvider>);
    expect(html).toContain("正在加载");
    expect(html).not.toContain('aria-label="新用户引导"');
    expect(html).not.toContain('class="app-shell');
    client.clear();
  });

  it("opens the workspace when the persisted version matches", () => {
    vi.stubGlobal("document", { cookie: "laxcode_guest_id=existing-guest" });
    const client = new QueryClient();
    client.setQueryData(["user-config", "existing-guest"], { user_guide_version: "same", user_guide_seen_version: "same" });
    const html = renderToStaticMarkup(<QueryClientProvider client={client}><App /></QueryClientProvider>);
    expect(html).not.toContain('aria-label="新用户引导"');
    expect(html).toContain('class="app-shell');
    client.clear();
  });
});
