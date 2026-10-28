import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { isValidElement, type ReactElement, type ReactNode } from "react";
import { ChatFiles } from "./ChatFiles";
import { FilePreview } from "./FilePreview";

// Exercise the pointer handoff timers without introducing a DOM test dependency.
const hooks = vi.hoisted(() => ({ states: [] as unknown[], refs: [] as { current: unknown }[], stateIndex: 0, refIndex: 0 }));
vi.mock("react", async original => ({
  ...await original<typeof import("react")>(),
  useState: (initial: unknown) => {
    const i = hooks.stateIndex++;
    if (!(i in hooks.states)) hooks.states[i] = initial;
    return [hooks.states[i], (next: unknown) => { hooks.states[i] = next; }];
  },
  useRef: (initial: unknown) => {
    const i = hooks.refIndex++;
    return hooks.refs[i] ?? (hooks.refs[i] = { current: initial });
  },
  useId: () => "test-id",
  useCallback: (fn: unknown) => fn,
  useEffect: vi.fn(), useLayoutEffect: vi.fn(),
}));
vi.mock("react-dom", () => ({ createPortal: (children: ReactNode) => children }));

beforeEach(() => { hooks.states = []; hooks.refs = []; vi.useFakeTimers(); vi.stubGlobal("document", { body: {} }); });
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

type Element = ReactElement<Record<string, unknown>>;
function elements(node: ReactNode): Element[] {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (!isValidElement<Record<string, unknown>>(node)) return [];
  return [node, ...elements(node.props.children as ReactNode)];
}
function render() {
  hooks.stateIndex = 0; hooks.refIndex = 0;
  return elements(ChatFiles({ sessionID: "s", chatID: "c", files: [{ path: "demo/a.txt", change_type: "created" }] }));
}
function find(className: string) { return render().find(el => el.props.className === className)!; }
function event(el: Element, name: string) { (el.props[name] as (event: unknown) => void)({ currentTarget: {} }); }
function hasList() { return Boolean(find("chat-file-popover")); }
function preview() { return render().find(el => el.type === FilePreview); }

it("keeps the list and preview open while moving between their hover regions", () => {
  event(find("chat-files-count"), "onMouseEnter");
  expect(hasList()).toBe(true);
  event(find("chat-files-count"), "onMouseLeave");
  vi.advanceTimersByTime(150);
  event(find("chat-file-popover"), "onMouseEnter");
  vi.advanceTimersByTime(400);
  expect(hasList()).toBe(true);
  event(find("file-preview-trigger"), "onMouseEnter");
  vi.advanceTimersByTime(299);
  expect(preview()).toBeUndefined();
  vi.advanceTimersByTime(1);
  expect(preview()).toBeDefined();
  event(find("chat-file-popover"), "onMouseLeave");
  vi.advanceTimersByTime(150);
  event(preview()!, "onMouseEnter");
  vi.advanceTimersByTime(400);
  expect(hasList()).toBe(true);
  expect(preview()).toBeDefined();
  event(preview()!, "onMouseLeave");
  vi.advanceTimersByTime(300);
  expect(hasList()).toBe(false);
  expect(preview()).toBeUndefined();
});

it("cancels a preview when the pointer quickly passes over a filename", () => {
  event(find("chat-files-count"), "onMouseEnter");
  event(find("file-preview-trigger"), "onMouseEnter");
  event(find("file-preview-trigger"), "onMouseLeave");
  vi.advanceTimersByTime(500);
  expect(preview()).toBeUndefined();
});

it("pins a clicked list and opens a clicked file immediately for touch users", () => {
  event(find("chat-files-count"), "onFocus");
  event(find("chat-files-count"), "onClick");
  event(find("chat-files-count"), "onMouseLeave");
  vi.advanceTimersByTime(500);
  expect(hasList()).toBe(true);
  event(find("file-preview-trigger"), "onClick");
  expect(preview()).toBeDefined();
  event(preview()!, "onMouseLeave");
  vi.advanceTimersByTime(500);
  expect(preview()).toBeDefined();
  event(find("chat-files-count"), "onClick");
  expect(hasList()).toBe(false);
  expect(preview()).toBeUndefined();
});
