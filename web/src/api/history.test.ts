import { afterEach, expect, it, vi } from "vitest";
import { downloadChatFiles } from "./history";

afterEach(() => vi.unstubAllGlobals());

it("downloads the chat ZIP without submitting client file paths", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response("zip-content", { headers: { "Content-Type": "application/zip" } }));
  vi.stubGlobal("fetch", fetch);
  const blob = await downloadChatFiles("session/one", "chat/one");
  expect(await blob.text()).toBe("zip-content");
  expect(fetch).toHaveBeenCalledWith("/api/sessions/session%2Fone/chats/chat%2Fone/files.zip");
});

it("reports missing files to the file card", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "文件已删除：/demo/a.txt" }), { status: 409 })));
  await expect(downloadChatFiles("session", "chat")).rejects.toMatchObject({ status: 409, message: "文件已删除：/demo/a.txt" });
});
