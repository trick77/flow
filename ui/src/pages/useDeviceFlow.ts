import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  errorCode,
  pollGitHubDeviceFlow,
  startGitHubDeviceFlow,
} from "../api";
import type { ErrorCode, GitHubConnection } from "../api/types.gen";
import {
  activeFlow,
  nextInterval,
  pollOutcome,
  type ActiveFlow,
  type PollOutcome,
} from "./github";

// FlowState is where a device flow stands, as the Settings row shows it.
export type FlowState =
  | { phase: "idle" }
  | { phase: "starting" }
  | { phase: "waiting"; flow: ActiveFlow }
  | { phase: "connected"; connection: GitHubConnection }
  | { phase: "expired" }
  | { phase: "denied" }
  | { phase: "unavailable" }
  | { phase: "failed"; code: ErrorCode };

// HIDDEN_GRACE_MS: polling goes on this long in a hidden tab, then pauses
// until the tab is visible again.
export const HIDDEN_GRACE_MS = 60_000;

// kept is the code out for confirmation while the Settings page is not shown:
// leaving the page stops polling, coming back within the code's lifetime
// resumes it with the same code (the API cannot hand it out again). Memory
// only: a reload starts over.
let kept: ActiveFlow | null = null;

// forgetDeviceFlow drops the kept code (tests; a new page load has none).
export function forgetDeviceFlow() {
  kept = null;
}

function initial(): FlowState {
  if (kept && kept.expiresAt > Date.now()) {
    return { phase: "waiting", flow: kept };
  }
  kept = null;
  return { phase: "idle" };
}

function ended(o: PollOutcome): FlowState {
  switch (o.kind) {
    case "connected":
      return { phase: "connected", connection: o.connection };
    case "expired":
      return { phase: "expired" };
    case "denied":
      return { phase: "denied" };
    case "unavailable":
      return { phase: "unavailable" };
    case "failed":
      return { phase: "failed", code: o.code };
    default:
      // pending, slowDown and retry keep the loop going; never here.
      return { phase: "failed", code: "internal" };
  }
}

export interface DeviceFlowControl {
  state: FlowState;
  // remainingMs is the code's time left while waiting, else 0.
  remainingMs: number;
  // paused: the tab was hidden long enough that polling stopped.
  paused: boolean;
  start: () => void;
  cancel: () => void;
  // reset forgets an ended flow's outcome (back to idle).
  reset: () => void;
}

// useDeviceFlow runs GitHub's device flow: start asks for a code, then the
// server is polled at the interval it gave, one request at a time, the wait
// counted from each answer. A slow-down lengthens the interval for good; a
// lost answer is retried at the same interval. Polling stops on success,
// denial, the server's expiry (or a lost answer past the local deadline,
// counted from the seconds left the start answered), cancel, unmount, and after
// HIDDEN_GRACE_MS in a hidden tab; a visible tab resumes it, waiting out what
// is left of the interval. onConnected gets the connection the poll answered.
export function useDeviceFlow(
  onConnected: (c: GitHubConnection) => void,
): DeviceFlowControl {
  const [state, setState] = useState<FlowState>(initial);
  const [now, setNow] = useState(() => Date.now());
  const [paused, setPaused] = useState(false);
  const notify = useRef(onConnected);
  useEffect(() => {
    notify.current = onConnected;
  }, [onConnected]);
  // A start whose answer comes after a cancel or unmount is dropped.
  const startGen = useRef(0);

  const waiting = state.phase === "waiting" ? state.flow : null;

  // The poll loop, one per code.
  useEffect(() => {
    if (!waiting) return;
    const flow = waiting;
    let intervalMs = flow.intervalMs;
    let last = Date.now();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let hideTimer: ReturnType<typeof setTimeout> | undefined;
    let polling = false;
    let stopped = false;
    let isPaused = false;

    const finish = (next: FlowState) => {
      stopped = true;
      clearTimeout(timer);
      clearTimeout(hideTimer);
      kept = null;
      setPaused(false);
      setState(next);
    };
    const schedule = () => {
      clearTimeout(timer);
      if (stopped || polling || isPaused) return;
      timer = setTimeout(poll, Math.max(0, last + intervalMs - Date.now()));
    };
    // The server's expired answer ends the code; the local deadline only
    // shows the countdown, and ends the flow when the server cannot be
    // reached past it.
    const poll = () => {
      polling = true;
      pollGitHubDeviceFlow()
        .then(
          (a) => pollOutcome(a),
          (err: unknown) => pollOutcome(undefined, err),
        )
        .then((o) => {
          polling = false;
          if (stopped) return;
          last = Date.now();
          if (o.kind === "retry" && last >= flow.expiresAt) {
            finish({ phase: "expired" });
            return;
          }
          if (
            o.kind === "pending" ||
            o.kind === "slowDown" ||
            o.kind === "retry"
          ) {
            intervalMs = nextInterval(intervalMs, o);
            // A page shown again resumes at the lengthened interval.
            if (kept?.userCode === flow.userCode)
              kept = { ...flow, intervalMs };
            schedule();
            return;
          }
          if (o.kind === "connected") notify.current(o.connection);
          finish(ended(o));
        });
    };
    const onVisibility = () => {
      clearTimeout(hideTimer);
      if (document.hidden) {
        hideTimer = setTimeout(() => {
          isPaused = true;
          clearTimeout(timer);
          setPaused(true);
        }, HIDDEN_GRACE_MS);
      } else if (isPaused) {
        isPaused = false;
        setPaused(false);
        schedule();
      }
    };

    document.addEventListener("visibilitychange", onVisibility);
    onVisibility();
    schedule();
    return () => {
      stopped = true;
      clearTimeout(timer);
      clearTimeout(hideTimer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [waiting]);

  // The countdown, once a second while waiting, until the local deadline.
  useEffect(() => {
    if (!waiting) return;
    const tick = setInterval(() => {
      const t = Date.now();
      setNow(t);
      if (t >= waiting.expiresAt) clearInterval(tick);
    }, 1000);
    return () => clearInterval(tick);
  }, [waiting]);

  // Leaving the page drops a start still under way.
  useEffect(
    () => () => {
      startGen.current++;
    },
    [],
  );

  const start = useCallback(() => {
    const gen = ++startGen.current;
    kept = null;
    setPaused(false);
    setState({ phase: "starting" });
    startGitHubDeviceFlow().then(
      (f) => {
        if (gen !== startGen.current) return;
        const at = Date.now();
        const flow = activeFlow(f, at);
        kept = flow;
        setNow(at);
        setState({ phase: "waiting", flow });
      },
      (err: unknown) => {
        if (gen !== startGen.current) return;
        setState(
          err instanceof ApiError && err.code === "not_implemented"
            ? { phase: "unavailable" }
            : { phase: "failed", code: errorCode(err) },
        );
      },
    );
  }, []);

  const cancel = useCallback(() => {
    startGen.current++;
    kept = null;
    setPaused(false);
    setState({ phase: "idle" });
  }, []);

  const reset = useCallback(() => setState({ phase: "idle" }), []);

  return {
    state,
    remainingMs: waiting ? Math.max(0, waiting.expiresAt - now) : 0,
    paused,
    start,
    cancel,
    reset,
  };
}
