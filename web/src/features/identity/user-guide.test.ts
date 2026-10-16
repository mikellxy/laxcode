import { afterEach, describe, expect, it, vi } from "vitest";
import { hasSeenUserGuide, markUserGuideSeen } from "./user-guide";

afterEach(() => vi.unstubAllGlobals());

describe("user guide cookie", () => {
  it.each(["", "laxcode_guide_seen=0", "other_laxcode_guide_seen=1", "other=%invalid"])("shows the guide unless the exact seen cookie is set: %s", (cookie) => {
    vi.stubGlobal("document", { cookie });
    expect(hasSeenUserGuide()).toBe(false);
  });

  it("recognizes the seen flag alongside the guest cookie", () => {
    vi.stubGlobal("document", { cookie: "laxcode_guest_id=guest; laxcode_guide_seen=1; theme=light" });
    expect(hasSeenUserGuide()).toBe(true);
  });

  it.each(["http:", "https:"])("records completion for a year with the correct cookie flags on %s", (protocol) => {
    const document = { cookie: "" };
    vi.stubGlobal("document", document);
    vi.stubGlobal("location", { protocol });
    markUserGuideSeen();
    expect(document.cookie).toContain("laxcode_guide_seen=1; Path=/; Max-Age=31536000; SameSite=Lax");
    expect(document.cookie.includes("Secure")).toBe(protocol === "https:");
    expect(hasSeenUserGuide()).toBe(true);
  });
});
