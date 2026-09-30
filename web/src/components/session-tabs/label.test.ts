import { describe, expect, it } from "vitest";
import { contextUsageLabel, sessionLabel } from "./label";

describe("sessionLabel", () => {
  it("numbers newest-first sessions by creation order", () => {
    expect(sessionLabel("", 0, 2)).toBe("会话 02");
    expect(sessionLabel("", 1, 2)).toBe("会话 01");
  });

  it("keeps an explicit session title", () => {
    expect(sessionLabel("  修复登录问题  ", 0, 2)).toBe("修复登录问题");
  });
});

describe("contextUsageLabel", () => {
  it("keeps enough precision for large context windows", () => {
    expect(contextUsageLabel(17971, 1048576)).toBe("1.71% used");
  });

  it("does not add decimals when the percentage is exact", () => {
    expect(contextUsageLabel(64000, 128000)).toBe("50% used");
  });

  it("keeps a non-zero usage visible below one basis point", () => {
    expect(contextUsageLabel(1, 1048576)).toBe("<0.01% used");
  });
});
