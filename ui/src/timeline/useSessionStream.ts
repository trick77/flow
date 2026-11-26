import { useEffect, useReducer, useRef } from "react";

import { streamSessionEvents, type EventSourceCtor } from "../api";
import type { SessionStatus } from "../contract/events.gen";
import { emptyStream, reduceStream, type StreamState } from "./state";

// useSessionStream follows one session's event stream for as long as the
// component lives; mount it with key={sessionId} so a switch starts empty.
// onStatus hears every session.status as it arrives.
export function useSessionStream(
  sessionId: string,
  opts: {
    EventSource?: EventSourceCtor;
    onStatus?: (status: SessionStatus) => void;
  } = {},
): StreamState {
  const [state, dispatch] = useReducer(reduceStream, emptyStream);
  const onStatus = useRef(opts.onStatus);
  useEffect(() => {
    onStatus.current = opts.onStatus;
  });
  const { EventSource } = opts;
  useEffect(
    () =>
      streamSessionEvents(sessionId, {
        EventSource,
        onOpen: () => dispatch({ type: "open" }),
        onEvent: (event) => {
          dispatch({ type: "event", event });
          if (event.kind === "session.status") {
            onStatus.current?.(event.data.status);
          }
        },
      }),
    [sessionId, EventSource],
  );
  return state;
}
