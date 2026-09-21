import { describe, expect, it } from "vitest";
import { shouldSubmitOnKeyDown } from "./keyboard";

const event = (overrides: Partial<Parameters<typeof shouldSubmitOnKeyDown>[0]> = {}) => ({
  key: "Enter",
  shiftKey: false,
  isComposing: false,
  keyCode: 13,
  ...overrides,
});

describe("shouldSubmitOnKeyDown", () => {
  it("submits a regular Enter key", () => {
    expect(shouldSubmitOnKeyDown(event())).toBe(true);
  });

  it("does not submit while an IME candidate is being confirmed", () => {
    expect(shouldSubmitOnKeyDown(event({ isComposing: true }))).toBe(false);
    expect(shouldSubmitOnKeyDown(event({ keyCode: 229 }))).toBe(false);
  });

  it("does not submit Shift+Enter or unrelated keys", () => {
    expect(shouldSubmitOnKeyDown(event({ shiftKey: true }))).toBe(false);
    expect(shouldSubmitOnKeyDown(event({ key: "a", keyCode: 65 }))).toBe(false);
  });
});
