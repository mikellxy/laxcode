import { afterEach, expect, it, vi } from "vitest";
import { getOrCreateGuestID } from "./guest";

afterEach(() => vi.unstubAllGlobals());

it("creates and retains a guest cookie on LAN HTTP without randomUUID", () => {
  const document = { cookie: "" };
  vi.stubGlobal("document", document);
  vi.stubGlobal("location", { protocol: "http:" });
  const getRandomValues = vi.fn((bytes: Uint8Array) => bytes.fill(0));
  vi.stubGlobal("crypto", { getRandomValues });
  const id = getOrCreateGuestID();
  expect(id).toBe("00000000-0000-4000-8000-000000000000");
  expect(document.cookie).toContain(`laxcode_guest_id=${id}`);
  expect(document.cookie).not.toContain("Secure");
  expect(getOrCreateGuestID()).toBe(id);
  expect(getRandomValues).toHaveBeenCalledOnce();
});
