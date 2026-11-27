import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, removeQueued, sendQueuedNow } from "../api";
import type { ErrorCode } from "../api/types.gen";

// What the bar shows of a queued message (the timeline's QueuedMessage).
export interface Queued {
  messageId: string;
  text: string;
}

// QueueBar lists the messages waiting while a turn runs, above the composer:
// Send now interrupts the turn and sends one at once, Remove drops it. A
// message leaves the bar when the stream says it was dequeued; until then its
// buttons stay disabled after a click.
export function QueueBar({
  sessionId,
  queue,
}: {
  sessionId: string;
  queue: readonly Queued[];
}) {
  const { t } = useTranslation("composer");
  // Messages an action is under way or done for: their buttons stay off
  // until the stream drops them from the queue.
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  const [error, setError] = useState<ErrorCode | null>(null);
  if (queue.length === 0) return null;

  const act = (
    messageId: string,
    call: (sessionId: string, messageId: string) => Promise<void>,
  ) => {
    setBusy((b) => new Set(b).add(messageId));
    setError(null);
    call(sessionId, messageId).catch((err: unknown) => {
      setBusy((b) => {
        const next = new Set(b);
        next.delete(messageId);
        return next;
      });
      setError(errorCode(err));
    });
  };

  return (
    <div className="queue" aria-label={t("queueLabel")} role="group">
      {queue.map((q) => (
        <div className="queued" key={q.messageId}>
          <span className="lbl">{t("queued")}</span>
          <span className="txt" title={q.text}>
            {q.text}
          </span>
          <span className="acts">
            <button
              className="btn sm"
              type="button"
              disabled={busy.has(q.messageId)}
              onClick={() => act(q.messageId, sendQueuedNow)}
            >
              {t("sendNow")}
            </button>
            <button
              className="btn sm ghost"
              type="button"
              disabled={busy.has(q.messageId)}
              onClick={() => act(q.messageId, removeQueued)}
            >
              {t("remove")}
            </button>
          </span>
        </div>
      ))}
      {error && (
        <p className="comp-error queue-error" role="alert">
          {t(error, { ns: "errors" })}
        </p>
      )}
    </div>
  );
}
