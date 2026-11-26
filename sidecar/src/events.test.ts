import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import {
  COMMAND_KINDS,
  EPHEMERAL_EVENT_KINDS,
  EVENT_KINDS,
  SIDECAR_COMMAND_KINDS,
  type EventDraft,
  type TypedEvent,
} from "./contract/events.gen.js";
import { decodeCommand, draft, encodeDraft } from "./events.js";

// The shared golden fixtures (contract/fixtures), as the Go and UI suites read
// them.
function fixture(name: string): string[] {
  const path = fileURLToPath(
    new URL(`../../contract/fixtures/${name}`, import.meta.url),
  );
  return readFileSync(path, "utf8")
    .split("\n")
    .filter((l) => l.trim() !== "");
}

describe("event fixtures", () => {
  const events = fixture("events.jsonl").map(
    (l) => JSON.parse(l) as TypedEvent,
  );

  it("cover every event kind of the contract", () => {
    const kinds = new Set(events.map((e) => e.kind));
    for (const k of EVENT_KINDS) {
      expect(kinds, k).toContain(k);
    }
  });

  it("carry seq exactly on durable kinds", () => {
    for (const e of events) {
      expect(e.seq === undefined, e.kind).toBe(
        EPHEMERAL_EVENT_KINDS.includes(e.kind),
      );
    }
  });

  it("round-trip as drafts through encodeDraft", () => {
    for (const e of events) {
      const d: EventDraft = { kind: e.kind, data: e.data };
      if (e.parentTool !== undefined) d.parentTool = e.parentTool;
      expect(JSON.parse(encodeDraft(d))).toEqual(d);
    }
  });
});

describe("encodeDraft", () => {
  it("writes one line, newlines inside values escaped", () => {
    const line = encodeDraft(
      draft("assistant.text", { blockId: "b1", text: "a\nb" }, { cmd: "c1" }),
    );
    expect(line.indexOf("\n")).toBe(line.length - 1);
    expect(JSON.parse(line)).toEqual({
      kind: "assistant.text",
      data: { blockId: "b1", text: "a\nb" },
      cmd: "c1",
    });
  });

  it("refuses a kind outside the contract", () => {
    expect(() =>
      encodeDraft({ kind: "nope" as EventDraft["kind"], data: {} }),
    ).toThrow("not in the contract");
  });
});

describe("decodeCommand", () => {
  const commands = fixture("commands.jsonl");

  it("covers every command kind and passes exactly the sidecar ones", () => {
    const kinds = new Set<string>();
    for (const line of commands) {
      const raw = JSON.parse(line) as { kind: string };
      kinds.add(raw.kind);
      const c = decodeCommand(line);
      if (SIDECAR_COMMAND_KINDS.includes(raw.kind as never)) {
        expect(c, raw.kind).toEqual(JSON.parse(line));
      } else {
        expect(c, raw.kind).toBeNull();
      }
    }
    for (const k of COMMAND_KINDS) {
      expect(kinds, k).toContain(k);
    }
  });

  it("skips newer versions, unknown kinds and commands without an id", () => {
    expect(
      decodeCommand('{"v":2,"id":"c","kind":"turn.interrupt","data":{}}'),
    ).toBeNull();
    expect(
      decodeCommand('{"v":1,"id":"c","kind":"nope","data":{}}'),
    ).toBeNull();
    expect(
      decodeCommand('{"v":1,"kind":"turn.interrupt","data":{}}'),
    ).toBeNull();
  });

  it("fails on a line that is not a JSON object", () => {
    expect(() => decodeCommand("null")).toThrow("not a JSON object");
    expect(() => decodeCommand("{")).toThrow();
  });
});
