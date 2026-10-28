import { afterEach, expect, it, vi } from "vitest";
import { downloadChatFiles, readChatFile } from "./history";

afterEach(() => vi.unstubAllGlobals());

it("downloads the chat ZIP without submitting client file paths", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response("zip-content", { headers: { "Content-Type": "application/zip" } }));
  vi.stubGlobal("fetch", fetch);
  const { blob, filename } = await downloadChatFiles("session/one", "chat/one");
  expect(await blob.text()).toBe("zip-content");
  expect(filename).toBe("chat-files.zip");
  expect(fetch).toHaveBeenCalledWith("/api/sessions/session%2Fone/chats/chat%2Fone/files.zip", expect.objectContaining({headers: { "X-USERID": "11111111-1111-4111-8111-111111111111" }}));
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

vi.mock("../features/identity/guest", () => ({ getOrCreateGuestID: () => "11111111-1111-4111-8111-111111111111" }));

it("uses the server's Unicode ZIP name", async () => {
  const filename = "登录页优化_20261028_1430.zip";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("zip", { headers: { "Content-Disposition": `attachment; filename*=utf-8''${encodeURIComponent(filename)}` } })));
  expect((await downloadChatFiles("s", "c")).filename).toBe(filename);
});

it("downloads a single file with an encoded path without applying preview limits", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(new Uint8Array([0, 255, 1]), { headers: { "Content-Disposition": 'attachment; filename="output.pdf"' } }));
  vi.stubGlobal("fetch", fetch);
  const result = await downloadChatFiles("s", "c", "demo/你好 #?.pdf");
  const url = new URL(fetch.mock.calls[0][0], "http://local");
  expect(url.pathname).toBe("/api/sessions/s/chats/c/file");
  expect(url.searchParams.get("path")).toBe("demo/你好 #?.pdf");
  expect(url.searchParams.get("download")).toBe("1");
  expect(result.filename).toBe("output.pdf");
  expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(new Uint8Array([0, 255, 1]));
});

it("preserves escaped quotes in a downloaded filename", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("text", { headers: { "Content-Disposition": 'attachment; filename="a\\"b.txt"' } })));
  expect((await downloadChatFiles("s", "c", 'demo/a"b.txt')).filename).toBe('a"b.txt');
});
