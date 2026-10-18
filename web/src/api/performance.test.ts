import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./client";
import { getSessionPerformance } from "./performance";

afterEach(() => vi.unstubAllGlobals());

describe("performance API", () => {
  it("encodes the selected session and passes cancellation through to fetch", async () => {
    const data = { session_id: "session/one", chats: [], partial: false, invalid_line_count: 0, incomplete_trace_count: 0 };
    const controller = new AbortController();
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(data)));
    vi.stubGlobal("fetch", fetchMock);
    await expect(getSessionPerformance("session/one", controller.signal)).resolves.toEqual(data);
    expect(fetchMock).toHaveBeenCalledWith("/api/sessions/session%2Fone/performance", expect.objectContaining({ signal: controller.signal, cache: "no-store" }));
  });

  it("preserves the missing-log error code for the dialog's empty-log explanation", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "TRACE_LOG_UNAVAILABLE", message: "本地 tracing 日志不存在" }), { status: 404 })));
    const error = await getSessionPerformance("session").catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 404, code: "TRACE_LOG_UNAVAILABLE", message: "本地 tracing 日志不存在" });
  });
});

vi.mock("../features/identity/guest", () => ({ getOrCreateGuestID: () => "11111111-1111-4111-8111-111111111111" }));
