/// <reference types="node" />
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  EVENT_KINDS,
  PERMISSION_MODE_VALUES,
  SESSION_STATUS_VALUES,
} from "./events.gen";
import { isDurable, parseEvent } from "./events";

// The shared golden fixtures (contract/fixtures), as the Go and sidecar suites
// read them. vitest runs in ui/; a new URL(…, import.meta.url) would be turned
// into an asset import, which vite refuses outside the UI root.
function fixture(name: string): string[] {
  const path = join(process.cwd(), "..", "contract", "fixtures", name);
  return readFileSync(path, "utf8")
    .split("\n")
    .filter((l) => l.trim() !== "");
}

describe("parseEvent", () => {
  const lines = fixture("events.jsonl");

  it("reads every fixture, and the fixtures cover every kind", () => {
    const kinds = new Set<string>();
    for (const line of lines) {
      const e = parseEvent(line);
      expect(e, line).not.toBeNull();
      expect(e).toEqual(JSON.parse(line));
      kinds.add(e!.kind);
      expect(isDurable(e!.kind), e!.kind).toBe(e!.seq !== undefined);
    }
    expect([...kinds].sort()).toEqual([...EVENT_KINDS].sort());
  });

  it("skips newer versions, unknown kinds and non-objects", () => {
    expect(parseEvent('{"v":2,"kind":"notice","data":{}}')).toBeNull();
    expect(parseEvent('{"v":1,"kind":"future.kind","data":{}}')).toBeNull();
    expect(parseEvent('{"kind":"notice"}')).toBeNull();
    expect(parseEvent("null")).toBeNull();
    expect(parseEvent("7")).toBeNull();
  });

  it("narrows data by kind", () => {
    const e = parseEvent(
      '{"v":1,"seq":3,"ts":"2026-11-26T14:18:03Z","session":"s","kind":"gate.queued","data":{"position":2}}',
    );
    if (e?.kind !== "gate.queued") throw new Error("kind lost");
    expect(e.data.position).toBe(2);
  });

  it("orders permission modes from least to most permissive", () => {
    expect(PERMISSION_MODE_VALUES[0]).toBe("plan");
    expect(PERMISSION_MODE_VALUES[PERMISSION_MODE_VALUES.length - 1]).toBe(
      "full_access",
    );
    expect(SESSION_STATUS_VALUES).toContain("queued");
  });
});
