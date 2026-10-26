import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { CopyableID } from "./CopyableID";

vi.mock("react", async (original) => ({
  ...await original<typeof import("react")>(), useState: vi.fn(), useEffect: vi.fn(),
}));

const setFeedback = vi.fn();
const id = "a9875db7-4a04-4895-9b35-9214a27248eb";
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useState).mockReturnValue(["", setFeedback]);
});
afterEach(() => vi.unstubAllGlobals());

describe("copyable IDs", () => {
  it.each(["user_id", "session_id"] as const)("copies the complete %s without its label", async (label) => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const element = CopyableID({ label, value: id });
    await element.props.children[2].props.onClick();
    expect(writeText).toHaveBeenCalledWith(id);
    expect(setFeedback).toHaveBeenCalledWith("已复制");
    const html = renderToStaticMarkup(element);
    expect(html).toContain(`aria-label="复制 ${label}"`);
    expect(html).toContain(`title="${id}"`);
  });

  it.each(["denied", "unavailable"])("offers manual copying when clipboard is %s", async (failure) => {
    vi.stubGlobal("navigator", failure === "denied"
      ? { clipboard: { writeText: vi.fn().mockRejectedValue(new Error("Not allowed")) } } : {});
    await CopyableID({ label: "user_id", value: id }).props.children[2].props.onClick();
    expect(setFeedback).toHaveBeenCalledWith("复制失败，请手动选择复制");
    expect(setFeedback).not.toHaveBeenCalledWith("已复制");
  });
});
