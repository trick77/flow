import { describe, expect, it } from "vitest";

import { encodeEvent } from "./events.js";

describe("encodeEvent", () => {
  it("writes one JSON line that reads back as the event", () => {
    const event = { type: "text", text: "hello", seq: 1 };
    const line = encodeEvent(event);
    expect(line.endsWith("\n")).toBe(true);
    expect(JSON.parse(line)).toEqual(event);
  });

  it("keeps a newline inside a value on the same line", () => {
    const line = encodeEvent({ type: "text", text: "a\nb" });
    expect(line.indexOf("\n")).toBe(line.length - 1);
  });

  it("refuses an event without a type", () => {
    expect(() => encodeEvent({ type: "" })).toThrow("without a type");
  });
});
