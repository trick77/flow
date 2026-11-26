/// <reference types="node" />
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { parseEvent } from "../contract/events";
import type { EventPayloads, TypedEvent } from "../contract/events.gen";

// Test helpers for the timeline: the shared fixtures (read from ui/, as
// src/contract/events.test.ts does) and hand-made events.

export function fixtureLines(name: string): string[] {
  const path = join(process.cwd(), "..", "contract", "fixtures", name);
  return readFileSync(path, "utf8")
    .split("\n")
    .filter((l) => l.trim() !== "");
}

export function fixtureEvents(name: string): TypedEvent[] {
  return fixtureLines(name).map((l) => {
    const e = parseEvent(l);
    if (!e) throw new Error(`fixture line not readable: ${l}`);
    return e;
  });
}

export function mk<K extends keyof EventPayloads>(
  kind: K,
  data: EventPayloads[K],
  extra: { seq?: number; turn?: string; ts?: string; parentTool?: string } = {},
): TypedEvent {
  return {
    v: 1,
    ts: extra.ts ?? "2026-11-26T12:00:00Z",
    session: "s1",
    kind,
    data,
    ...(extra.seq === undefined ? {} : { seq: extra.seq }),
    ...(extra.turn === undefined ? {} : { turn: extra.turn }),
    ...(extra.parentTool === undefined ? {} : { parentTool: extra.parentTool }),
  } as TypedEvent;
}
