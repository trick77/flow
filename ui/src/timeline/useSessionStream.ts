import { useEffect, useReducer, useRef } from "react";

import { streamSessionEvents, type EventSourceCtor } from "../api";
import type { SessionStatus } from "../contract/events.gen";
import { emptyStream, reduceStream, type StreamState } from "./state";

// useSessionStream follows one session's event stream for as long as the
// component lives; mount it with key={sessionId} so a switch starts empty.
// onStatus hears each session.status past sinceSeq (the summary's lastSeq):
// the stream replays the log from its start, and a status the row has
// mirrored already, or overruled (a turn cut off by a sandbox stop), must not
// overwrite the row's.
export function useSessionStream(
  sessionId: string,
  opts: {
    EventSource?: EventSourceCtor;
    onStatus?: (status: SessionStatus) => void;
    sinceSeq?: number;
  } = {},
): StreamState {
  const [state, dispatch] = useReducer(reduceStream, emptyStream);
  const onStatus = useRef(opts.onStatus);
  const sinceSeq = useRef(opts.sinceSeq ?? 0);
  useEffect(() => {
    onStatus.current = opts.onStatus;
    sinceSeq.current = opts.sinceSeq ?? 0;
  });
  const { EventSource } = opts;
  useEffect(
    () =>
      streamSessionEvents(sessionId, {
        EventSource,
        onOpen: () => dispatch({ type: "open" }),
        onEvent: (event) => {
          dispatch({ type: "event", event });
          if (
            event.kind === "session.status" &&
            (event.seq ?? Infinity) > sinceSeq.current
          ) {
            onStatus.current?.(event.data.status);
          }
        },
      }),
    [sessionId, EventSource],
  );
  return state;
}
