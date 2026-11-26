import {
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { errorCode, interruptTurn, sendMessage } from "../api";
import type { ErrorCode } from "../api/types.gen";
import { Icon } from "../shell/Icon";

// Composer: the message box of a session. Enter sends, Shift+Enter breaks
// the line. While a turn runs a message queues (the server decides) and a
// stop button interrupts the turn.
export function Composer({
  sessionId,
  running,
}: {
  sessionId: string;
  running: boolean;
}) {
  const { t } = useTranslation("composer");
  const inputId = useId();
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  // One client id per draft: a retry after a lost answer reuses it, so the
  // server acknowledges it as a duplicate instead of sending the message twice.
  // It is renewed after a send succeeds and whenever the text changes.
  const draftId = useRef<string | null>(null);

  const edit = (value: string) => {
    draftId.current = null;
    setText(value);
  };

  const send = () => {
    const body = text.trim();
    if (body === "" || sending) return;
    setSending(true);
    setError(null);
    draftId.current ??= crypto.randomUUID();
    sendMessage(sessionId, { clientId: draftId.current, text: body })
      .then(() => edit(""))
      .catch((err: unknown) => setError(errorCode(err)))
      .finally(() => setSending(false));
  };

  const stop = () => {
    setStopping(true);
    setError(null);
    interruptTurn(sessionId)
      .catch((err: unknown) => setError(errorCode(err)))
      .finally(() => setStopping(false));
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    send();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      send();
    }
  };

  return (
    <div className="dock">
      <div className="dock-in">
        <form className="composer" onSubmit={onSubmit}>
          <label className="sr" htmlFor={inputId}>
            {t("placeholder")}
          </label>
          <textarea
            id={inputId}
            rows={2}
            placeholder={t("placeholder")}
            value={text}
            readOnly={sending}
            aria-busy={sending}
            onChange={(e) => edit(e.target.value)}
            onKeyDown={onKeyDown}
          />
          <div className="comp-bar">
            <span className="grow" />
            {running && (
              <button
                className="send stop"
                type="button"
                aria-label={t("stop")}
                disabled={stopping}
                onClick={stop}
              >
                <Icon name="stop" />
                <span>{t("stopLabel")}</span>
              </button>
            )}
            <button
              className="send"
              type="submit"
              aria-label={running ? t("queue") : t("send")}
              disabled={sending || text.trim() === ""}
            >
              <Icon name="arrowUp" />
            </button>
          </div>
        </form>
        {error && (
          <p className="comp-error" role="alert">
            {t(error, { ns: "errors" })}
          </p>
        )}
      </div>
    </div>
  );
}
