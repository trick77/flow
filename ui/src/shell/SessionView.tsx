import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  archiveSession,
  errorCode,
  sandboxAction,
  type EventSourceCtor,
} from "../api";
import type {
  ErrorCode,
  Sandbox,
  SandboxState,
  SessionStatus,
  SessionSummary,
} from "../api/types.gen";
import { Composer } from "../composer";
import {
  TerminalDrawer,
  TerminalToggle,
  type EmulatorFactory,
  type WebSocketCtor,
} from "../terminal";
import { foldEvents, Timeline, useSessionStream } from "../timeline";
import { Header } from "./Header";
import { Icon } from "./Icon";

// SandboxNotice stands in for a session's history while its sandbox does not
// run: the log lives in the sandbox, so the timeline stays empty until it
// starts. Start is the Projects page's start; the state that follows comes
// from the app stream. Mount it with key={state}: a new state ends "starting".
function SandboxNotice({
  projectId,
  state,
}: {
  projectId: string;
  state: SandboxState;
}) {
  const { t } = useTranslation("shell");
  const [starting, setStarting] = useState(false);
  const [failed, setFailed] = useState<ErrorCode | null>(null);
  const start = () => {
    setStarting(true);
    setFailed(null);
    sandboxAction(projectId, "start").catch((err: unknown) => {
      setFailed(errorCode(err));
      setStarting(false);
    });
  };
  const text =
    starting || state === "setup"
      ? t("sandboxStarting")
      : state === "failed"
        ? t("sandboxFailed")
        : state === "stopping"
          ? t("sandboxStopping")
          : state === "deleting"
            ? t("sandboxDeleting")
            : t("sandboxStopped");
  const startable =
    !starting &&
    (state === "stopped" ||
      state === "none" ||
      state === "failed" ||
      state === "stopping");
  return (
    <div className="confirm sandbox-notice" role="status">
      {text}
      {failed && (
        <p className="hdr-confirm-err" role="alert">
          {t(failed, { ns: "errors" })}
        </p>
      )}
      {startable && (
        <div className="row-inline">
          <button type="button" className="btn sm primary" onClick={start}>
            {t("startSandbox")}
          </button>
        </div>
      )}
    </div>
  );
}

// SessionView is one session: header, timeline, composer, terminal drawer.
// Mount it with key={session.id}: a switch starts a fresh stream.
export function SessionView({
  session,
  sandbox,
  projectName,
  EventSource,
  WebSocket,
  createEmulator,
  onStatus,
  onArchived,
  nav,
  hidden,
}: {
  session: SessionSummary;
  // sandbox is the project's, as the app stream keeps it.
  sandbox?: Sandbox;
  projectName?: string;
  EventSource?: EventSourceCtor;
  WebSocket?: WebSocketCtor;
  createEmulator?: EmulatorFactory;
  onStatus?: (status: SessionStatus) => void;
  // onArchived is told once the session is archived (or found gone).
  onArchived?: () => void;
  // nav is the header's Projects and Settings buttons, after the toggles.
  nav?: ReactNode;
  // hidden keeps the view mounted while a page shows: the composer's draft,
  // the terminal and the stream stay as they are.
  hidden?: boolean;
}) {
  const { t } = useTranslation("shell");
  // A sandbox that does not run ends its turns without a turn.ended in the
  // log: the session's row (Flow ends the turn there) tells the truth, the
  // stream's last status does not, nor may it overwrite the row's.
  const downState = sandbox?.state !== "warm" ? sandbox?.state : undefined;
  const down = downState !== undefined;
  const stream = useSessionStream(session.id, {
    EventSource,
    onStatus: down ? undefined : onStatus,
  });
  const folded = useMemo(() => foldEvents(stream), [stream]);
  const status = down ? session.status : (folded.status ?? session.status);
  const [terminalOpen, setTerminalOpen] = useState(false);
  const [asking, setAsking] = useState(false);
  const [archiving, setArchiving] = useState(false);
  const [failed, setFailed] = useState<ErrorCode | null>(null);
  const archive = () => {
    setArchiving(true);
    setFailed(null);
    archiveSession(session.id).then(
      () => onArchived?.(),
      (err: unknown) => {
        const code = errorCode(err);
        if (code === "not_found") {
          onArchived?.();
          return;
        }
        setFailed(code);
        setArchiving(false);
      },
    );
  };
  const tab = useMemo(
    () => ({
      key: `${session.id}-1`,
      projectId: session.projectId,
      sessionId: session.id,
    }),
    [session.id, session.projectId],
  );
  return (
    <section className="view" aria-label={t("session")} hidden={hidden}>
      <Header
        project={projectName}
        title={session.title}
        branch={session.branch}
        actions={
          <>
            <TerminalToggle
              pressed={terminalOpen}
              onToggle={() => setTerminalOpen((o) => !o)}
            />
            <button
              type="button"
              className="icon-btn"
              aria-label={t("archive")}
              title={t("archive")}
              aria-expanded={asking}
              onClick={() => {
                setAsking((a) => !a);
                setFailed(null);
              }}
            >
              <Icon name="archive" />
            </button>
            {nav}
          </>
        }
      />
      {asking && (
        <div
          className="confirm hdr-confirm"
          role="alertdialog"
          aria-label={t("archive")}
        >
          {t("confirmArchive", { title: session.title })}
          {failed && (
            <p className="hdr-confirm-err" role="alert">
              {t(failed, { ns: "errors" })}
            </p>
          )}
          <div className="row-inline">
            <button
              type="button"
              className="btn sm danger"
              disabled={archiving}
              onClick={archive}
            >
              {archiving ? t("archiving") : t("confirmArchiveAction")}
            </button>
            <button
              type="button"
              className="btn sm ghost"
              disabled={archiving}
              onClick={() => setAsking(false)}
            >
              {t("cancel")}
            </button>
          </div>
        </div>
      )}
      <div className="center-body">
        {downState && (
          <SandboxNotice
            key={downState}
            projectId={session.projectId}
            state={downState}
          />
        )}
        <Timeline
          sessionId={session.id}
          folded={folded}
          working={status === "working"}
          provider={session.provider}
        />
        <Composer
          sessionId={session.id}
          running={
            down
              ? status === "working"
              : folded.turnOpen || status === "working"
          }
        />
        <TerminalDrawer
          tab={tab}
          open={terminalOpen}
          onClose={() => setTerminalOpen(false)}
          WebSocket={WebSocket}
          createEmulator={createEmulator}
        />
      </div>
    </section>
  );
}
