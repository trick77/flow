// Flow's event stream: newline-delimited JSON, one event per line. The sidecar
// will translate the Claude Agent SDK's messages into these events.

// FlowEvent is one event: its type plus whatever fields that type carries.
export interface FlowEvent {
  type: string;
  [field: string]: unknown;
}

// encodeEvent is the event as one line of the stream. JSON escapes every
// newline inside a value, so the only one is the terminator.
export function encodeEvent(event: FlowEvent): string {
  if (event.type === "") {
    throw new Error("flow event without a type");
  }
  return `${JSON.stringify(event)}\n`;
}
