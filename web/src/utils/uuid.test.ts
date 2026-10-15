import { afterEach, describe, expect, it, vi } from "vitest";
import { randomUUID } from "./uuid";

afterEach(() => vi.unstubAllGlobals());

describe("randomUUID", () => {
  it("uses the native method when available", () => {
    const native = vi.fn(() => "native-uuid");
    vi.stubGlobal("crypto", { randomUUID: native });
    expect(randomUUID()).toBe("native-uuid");
    expect(native).toHaveBeenCalledOnce();
  });

  it("generates a v4 UUID on HTTP without randomUUID", () => {
    vi.stubGlobal("crypto", { getRandomValues: (bytes: Uint8Array) => bytes.fill(255) });
    expect(randomUUID()).toBe("ffffffff-ffff-4fff-bfff-ffffffffffff");
  });

  it("requests fresh randomness for each UUID", () => {
    let sequence = 0;
    const getRandomValues = vi.fn((bytes: Uint8Array) => bytes.fill(sequence++));
    vi.stubGlobal("crypto", { getRandomValues });
    const first = randomUUID();
    const second = randomUUID();
    expect(first).not.toBe(second);
    expect(getRandomValues).toHaveBeenCalledTimes(2);
    for (const value of [first, second]) expect(value).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });
});
