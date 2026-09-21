import { describe, expect, it } from "vitest";
import { parseSSEFrame } from "./chat-stream";

describe("parseSSEFrame", () => {
  it("parses a named JSON event", () => expect(parseSSEFrame('event: message\ndata: {"delta":"你好"}')).toEqual({ type: "message", data: { delta: "你好" } }));
  it("joins multiline data", () => expect(parseSSEFrame('event: error\ndata: {"message":\ndata: "boom"}')).toEqual({ type: "error", data: { message: "boom" } }));
  it("ignores malformed and unknown events", () => { expect(parseSSEFrame("event: ping\ndata: {}" )).toBeNull(); expect(parseSSEFrame("event: done\ndata: nope")).toBeNull(); });
});
