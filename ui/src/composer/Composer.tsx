import {
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type Ref,
} from "react";
import { useTranslation } from "react-i18next";

import { errorCode, interruptTurn, replyQuestion, sendMessage } from "../api";
import type {
  AgentCatalog,
  Effort,
  ErrorCode,
  PermissionMode,
  Provider,
} from "../api/types.gen";
import { Icon } from "../shell/Icon";
import "./composer.css";
import { QueueBar, type Queued } from "./QueueBar";
import { SessionPickers, type SettingsPatch } from "./SessionPickers";

// Composer: the message box of a session. Enter sends, Shift+Enter breaks
// the line. While a turn runs a message queues (the server decides) and a
// stop button interrupts the turn; the queue shows above the box. While the
// agent waits on a question, the message answers it instead (the reply's
// text, docs/events.md "question with options, Answer in the composer").
export function Composer({
  sessionId,
  running,
  provider,
  model,
  effort,
  mode,
  modeCap,
  catalog,
  projectName,
  queue = [],
  questionId,
  inputRef,
  onUpdated,
}: {
  sessionId: string;
  running: boolean;
  // projectName names the sandbox in the note under the box.
  projectName?: string;
  // The session's agent, model, effort and permission mode: pickers in the bar.
  provider?: Provider;
  model?: string;
  effort?: Effort;
  mode?: PermissionMode;
  modeCap?: PermissionMode;
  catalog?: AgentCatalog;
  queue?: readonly Queued[];
  // The request id of the question waiting for an answer, if any.
  questionId?: string;
  inputRef?: Ref<HTMLTextAreaElement>;
  // A model, effort or mode change the server accepted.
  onUpdated?: (fields: SettingsPatch) => void;
}) {
  const { t } = useTranslation("composer");
  const inputId = useId();
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  const [settingsError, setSettingsError] = useState<ErrorCode | null>(null);
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
    // An answer to the waiting question goes to it, never as a message too.
    // On a refusal (already answered, expired) the draft stays for sending.
    const call = questionId
      ? replyQuestion(sessionId, questionId, { text: body })
      : sendMessage(sessionId, {
          clientId: (draftId.current ??= crypto.randomUUID()),
          text: body,
        });
    call
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

  const placeholder = questionId ? t("answerPlaceholder") : t("placeholder");
  return (
    <div className="dock">
      <div className="dock-in">
        <QueueBar sessionId={sessionId} queue={queue} />
        <form className="composer" onSubmit={onSubmit}>
          <label className="sr" htmlFor={inputId}>
            {placeholder}
          </label>
          <textarea
            id={inputId}
            ref={inputRef}
            rows={2}
            placeholder={placeholder}
            value={text}
            readOnly={sending}
            aria-busy={sending}
            onChange={(e) => edit(e.target.value)}
            onKeyDown={onKeyDown}
          />
          <div className="comp-bar">
            {provider && model && mode && (
              <SessionPickers
                sessionId={sessionId}
                provider={provider}
                model={model}
                effort={effort}
                mode={mode}
                modeCap={modeCap}
                catalog={catalog}
                turnRunning={running}
                onUpdated={onUpdated}
                onError={setSettingsError}
              />
            )}
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
              aria-label={
                questionId ? t("answer") : running ? t("queue") : t("send")
              }
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
        {settingsError && (
          <p className="comp-error" role="alert">
            {t("settingsFailed")} {t(settingsError, { ns: "errors" })}
          </p>
        )}
        {projectName && (
          <p className="comp-note">{t("runsIn", { project: projectName })}</p>
        )}
      </div>
    </div>
  );
}
