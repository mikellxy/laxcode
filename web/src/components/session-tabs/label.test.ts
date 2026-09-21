import { describe, expect, it } from "vitest";
import { sessionLabel } from "./label";

describe("sessionLabel", () => {
  it("numbers newest-first sessions by creation order", () => {
    expect(sessionLabel("", 0, 2)).toBe("会话 02");
    expect(sessionLabel("", 1, 2)).toBe("会话 01");
  });

  it("keeps an explicit session title", () => {
    expect(sessionLabel("  修复登录问题  ", 0, 2)).toBe("修复登录问题");
  });
});
