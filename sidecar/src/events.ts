// The sidecar's two streams, both newline-delimited JSON (docs/architecture.md,
// "Sidecar stdio"): commands arrive on stdin, one Command per line; Flow
// events leave on stdout as EventDraft lines, which the host numbers and logs.
// The types are generated from contract/events.schema.json.

import {
  COMMAND_KINDS,
  EVENT_KINDS,
  SIDECAR_COMMAND_KINDS,
  type CommandKind,
  type EventDraft,
  type EventKind,
  type EventPayloads,
  type TypedCommand,
} from "./contract/events.gen.js";

// VERSION is the contract's major version this build speaks.
export const VERSION = 1;

// draft builds a typed EventDraft: the payload type follows the kind.
export function draft<K extends EventKind>(
  kind: K,
  data: EventPayloads[K],
  extra: { parentTool?: string; cmd?: string } = {},
): EventDraft {
  return { kind, data, ...extra };
}

// encodeDraft is the draft as one stdout line. JSON escapes every newline
// inside a value, so the only one is the terminator.
export function encodeDraft(d: EventDraft): string {
  if (!EVENT_KINDS.includes(d.kind)) {
    throw new Error(`flow event kind ${String(d.kind)} is not in the contract`);
  }
  return `${JSON.stringify(d)}\n`;
}

// decodeCommand reads one stdin line. A command of a newer major version, of
// a kind this build does not know, or one the host should not have forwarded
// is skipped (null) rather than failing the stream.
export function decodeCommand(line: string): TypedCommand | null {
  const value: unknown = JSON.parse(line);
  if (typeof value !== "object" || value === null) {
    throw new Error("command is not a JSON object");
  }
  const c = value as { v?: unknown; kind?: unknown; id?: unknown };
  if (typeof c.v !== "number" || c.v > VERSION || typeof c.id !== "string") {
    return null;
  }
  const kind = c.kind as CommandKind;
  if (!COMMAND_KINDS.includes(kind) || !SIDECAR_COMMAND_KINDS.includes(kind)) {
    return null;
  }
  return value as TypedCommand;
}
