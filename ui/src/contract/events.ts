// Reading Flow events in the browser. The types are generated from
// contract/events.schema.json (events.gen.ts); the rules are docs/events.md.

import {
  EPHEMERAL_EVENT_KINDS,
  EVENT_KINDS,
  type EventKind,
  type TypedEvent,
} from "./events.gen";

// VERSION is the contract's major version this build reads.
export const VERSION = 1;

// parseEvent reads one SSE data field. An event of a newer major version or of
// a kind this build does not know is skipped (null): the server may be newer
// than the open tab.
export function parseEvent(data: string): TypedEvent | null {
  const value: unknown = JSON.parse(data);
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const e = value as { v?: unknown; kind?: unknown };
  if (typeof e.v !== "number" || e.v > VERSION) {
    return null;
  }
  if (!EVENT_KINDS.includes(e.kind as EventKind)) {
    return null;
  }
  return value as TypedEvent;
}

// isDurable: the event is in the log and carries a seq (a position to resume
// from); ephemeral ones are live only.
export function isDurable(kind: EventKind): boolean {
  return !EPHEMERAL_EVENT_KINDS.includes(kind);
}
