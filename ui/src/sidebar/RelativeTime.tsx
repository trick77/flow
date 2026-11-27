import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";

import { formatAgo, formatDateTime } from "../i18n/format";

// A minute clock shared by every relative time on screen: one timer, and a
// tick re-renders only the time labels, never the list around them. The
// snapshot is the time of the last tick, so it stays the same between ticks.
const TICK_MS = 60_000;
let now = Date.now();
let timer: ReturnType<typeof setTimeout> | null = null;
const listeners = new Set<() => void>();

function schedule() {
  // At the next full minute, so all labels change together.
  timer = setTimeout(
    () => {
      now = Date.now();
      listeners.forEach((fn) => fn());
      schedule();
    },
    TICK_MS - (Date.now() % TICK_MS),
  );
}

function subscribe(fn: () => void): () => void {
  if (listeners.size === 0) {
    now = Date.now();
    schedule();
  }
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0 && timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };
}

const snapshot = () => now;

// useMinuteClock is the time in ms, updated once a minute.
export function useMinuteClock(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

// RelativeTime is how long ago at was ("now", "4m", "2h", "3d"), the exact
// date and time in its tooltip.
export function RelativeTime({
  at,
  className,
}: {
  at: string;
  className?: string;
}) {
  const { t } = useTranslation("sidebar");
  const clock = useMinuteClock();
  const time = Date.parse(at);
  if (Number.isNaN(time)) return null;
  const date = new Date(time);
  return (
    <time className={className} dateTime={at} title={formatDateTime(date)}>
      {formatAgo(clock - time) ?? t("timeNow")}
    </time>
  );
}
