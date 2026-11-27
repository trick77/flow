import { useEffect, useRef, useState } from "react";

import { errorCode, markSessionSeen } from "../api";

// pageActive: the tab shows and has focus, so the owner can see the session.
export function pageActive(): boolean {
  return document.visibilityState === "visible" && document.hasFocus();
}

// useMarkSeen tells the server the owner has seen the open session up to
// lastSeq: while it is marked unseen, on screen (shown) and the tab is
// visible and focused. Calls are coalesced (at most one per delayMs, with the
// newest seq); after a failure it waits for the seq to advance, after
// not_implemented it stops for good. onSeen hears every accepted mark.
export function useMarkSeen({
  sessionId,
  lastSeq,
  unseen,
  shown,
  onSeen,
  delayMs = 1000,
}: {
  sessionId: string;
  lastSeq: number;
  unseen: boolean;
  shown: boolean;
  onSeen?: () => void;
  delayMs?: number;
}): void {
  const [active, setActive] = useState(pageActive);
  const latest = useRef(lastSeq);
  const sent = useRef(0);
  // A failed mark's seq: nothing is sent again until the log moves past it.
  const failedAt = useRef(0);
  const off = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seen = useRef(onSeen);
  useEffect(() => {
    latest.current = lastSeq;
    seen.current = onSeen;
  });

  useEffect(() => {
    const update = () => setActive(pageActive());
    window.addEventListener("focus", update);
    window.addEventListener("blur", update);
    document.addEventListener("visibilitychange", update);
    return () => {
      window.removeEventListener("focus", update);
      window.removeEventListener("blur", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);

  // Seen, then unseen again at the same seq (a sandbox stop cut the turn off:
  // no new event, the row says unseen): the same seq is marked again.
  useEffect(() => {
    if (!unseen) sent.current = 0;
  }, [unseen]);

  useEffect(() => {
    const want =
      unseen &&
      shown &&
      active &&
      !off.current &&
      lastSeq > 0 &&
      lastSeq > sent.current &&
      lastSeq > failedAt.current;
    if (!want || timer.current !== null) return;
    timer.current = setTimeout(() => {
      timer.current = null;
      const seq = latest.current;
      sent.current = seq;
      markSessionSeen(sessionId, seq).then(
        () => seen.current?.(),
        (err: unknown) => {
          sent.current = 0;
          failedAt.current = seq;
          if (errorCode(err) === "not_implemented") off.current = true;
        },
      );
    }, delayMs);
  }, [unseen, shown, active, lastSeq, sessionId, delayMs]);

  // Leaving (another session, a page, the tab): a pending mark is dropped.
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = null;
    },
    [shown, active, sessionId],
  );
}
