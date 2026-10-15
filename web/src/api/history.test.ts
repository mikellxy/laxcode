import { afterEach, expect, it, vi } from "vitest";
import { downloadChatFiles, readChatFile } from "./history";

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

it("reads a current text preview with an encoded project path and cancellation", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response("<script>literal text</script>", { headers: { "Content-Type": "text/plain; charset=utf-8", "X-Preview-Truncated": "true" } }));
  vi.stubGlobal("fetch", fetch);
  const controller = new AbortController();
  expect(await readChatFile("session/one", "chat/one", "demo/你好 #?.txt", controller.signal)).toEqual({ kind: "text", content: "<script>literal text</script>", truncated: true });
  const [url, options] = fetch.mock.calls[0];
  expect(url.split("?")[0]).toBe("/api/sessions/session%2Fone/chats/chat%2Fone/file");
  expect(new URLSearchParams(url.split("?")[1]).get("path")).toBe("demo/你好 #?.txt");
  expect(options).toMatchObject({ signal: controller.signal, cache: "no-store" });
});

it("returns verified image responses as blobs", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("image-data", { headers: { "Content-Type": "image/png" } })));
  const result = await readChatFile("s", "c", "demo/a.png", new AbortController().signal);
  expect(result.kind).toBe("image");
  if (result.kind === "image") expect(await result.blob.text()).toBe("image-data");
});

it("rejects active content types rather than rendering them as previews", async () => {
  for (const type of ["text/html", "image/svg+xml", "application/octet-stream"]) {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("unsafe", { headers: { "Content-Type": type } })));
    await expect(readChatFile("s", "c", "demo/a", new AbortController().signal)).rejects.toMatchObject({ status: 415 });
  }
});

it("reports server-side preview limits and file errors", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "图片超过 10 MiB，请下载后查看" }), { status: 413 })));
  await expect(readChatFile("s", "c", "demo/a.png", new AbortController().signal)).rejects.toMatchObject({ status: 413, message: "图片超过 10 MiB，请下载后查看" });
});
