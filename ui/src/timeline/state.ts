import { isDurable } from "../contract/events";
import type { OpenBlock, TypedEvent } from "../contract/events.gen";

// StreamState is what a session's event stream has delivered so far: the
// durable events in seq order and the text of blocks still being written.
export interface StreamState {
  events: TypedEvent[];
  lastSeq: number;
  // Open assistant blocks, in the order they started.
  live: OpenBlock[];
  // Blocks whose durable assistant.text has arrived; later deltas for them
  // are stale.
  done: Record<string, true>;
}

export const emptyStream: StreamState = {
  events: [],
  lastSeq: 0,
  live: [],
  done: {},
};

export type StreamAction =
  | { type: "event"; event: TypedEvent }
  // The stream (re)opened: open blocks are unknown until the next snapshot
  // or delta.
  | { type: "open" };

// reduceStream applies one frame. Durable events at or below lastSeq are
// replays and dropped; the stream helper drops them already, this keeps the
// state right whatever feeds it.
export function reduceStream(
  state: StreamState,
  action: StreamAction,
): StreamState {
  if (action.type === "open") {
    return state.live.length === 0 ? state : { ...state, live: [] };
  }
  const e = action.event;
  if (isDurable(e.kind)) {
    const seq = e.seq ?? state.lastSeq + 1;
    if (seq <= state.lastSeq) return state;
    const next: StreamState = {
      ...state,
      events: [...state.events, e],
      lastSeq: seq,
    };
    if (e.kind === "assistant.text") {
      const id = e.data.blockId;
      next.live = state.live.filter((b) => b.blockId !== id);
      next.done = { ...state.done, [id]: true };
    }
    // A turn that ended (lost, interrupted, the agent gone mid-stream) writes
    // no more of its open blocks: what was half streamed is not live text
    // under the turns that follow. Late deltas for them are stale.
    if (e.kind === "turn.ended" && state.live.length > 0) {
      next.live = [];
      next.done = { ...state.done };
      for (const b of state.live) next.done[b.blockId] = true;
    }
    return next;
  }
  switch (e.kind) {
    case "assistant.text_delta": {
      const { blockId, text } = e.data;
      if (state.done[blockId]) return state;
      const i = state.live.findIndex((b) => b.blockId === blockId);
      const live =
        i < 0
          ? [...state.live, { blockId, text }]
          : state.live.map((b, j) =>
              j === i ? { blockId, text: b.text + text } : b,
            );
      return { ...state, live };
    }
    case "live.snapshot":
      return {
        ...state,
        live: e.data.blocks.filter((b) => !state.done[b.blockId]),
      };
    default:
      // tool.progress and future ephemeral kinds: nothing to keep.
      return state;
  }
}
