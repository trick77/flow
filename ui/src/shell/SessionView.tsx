import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { useTranslation } from "react-i18next";

import {
  archiveSession,
  errorCode,
  getSession,
  sandboxAction,
  sandboxBusy,
  sandboxStale,
  sandboxStartable,
  updateSession,
  type EventSourceCtor,
} from "../api";
import type {
  AgentCatalog,
  ErrorCode,
  Sandbox,
  SandboxState,
  SessionStatus,
  SessionSummary,
  Worktree,
} from "../api/types.gen";
import { Composer } from "../composer";
import { PANEL_ID, ReviewPanel } from "../panel";
import type { SessionPatch } from "../sidebar";
import {
  TerminalDrawer,
  TerminalToggle,
  type EmulatorFactory,
  type WebSocketCtor,
} from "../terminal";
import { foldEvents, Timeline, useSessionStream } from "../timeline";
import { Header, type RepoEntry } from "./Header";
import { Icon } from "./Icon";
import { useMarkSeen } from "./useMarkSeen";
import { NARROW, useMediaQuery } from "./useMediaQuery";

// SandboxNotice stands in for a session's history while its sandbox does not
// run: the log lives in the sandbox, so the timeline stays empty until it
// starts. Start is the Projects page's start; the state that follows comes
// from the app stream. Mount it with key={state}: a new state ends "starting".
function SandboxNotice({
  projectId,
  state,
  busy,
}: {
  projectId: string;
  state: SandboxState;
  // busy: an operation runs on the sandbox now; no start is offered then.
  busy: boolean;
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
  const sb = { state, busy };
  const text = sandboxStale(sb)
    ? state === "setup"
      ? t("sandboxSetupStale")
      : t("sandboxStopStale")
    : state === "stopping"
      ? t("sandboxStopping")
      : state === "deleting"
        ? t("sandboxDeleting")
        : starting || busy
          ? t("sandboxStarting")
          : state === "failed"
            ? t("sandboxFailed")
            : t("sandboxStopped");
  const startable = !starting && sandboxStartable(sb);
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

// MoreMenu holds the diff panel toggle and Archive on phones (COMPACT), so
// the session title keeps its room in the header. Choosing an item closes
// the menu and puts the focus back on its button; Up and Down move, Escape
// closes.
function MoreMenu({
  buttonRef,
  panelOpen,
  onTogglePanel,
  onArchive,
}: {
  buttonRef: RefObject<HTMLButtonElement | null>;
  panelOpen: boolean;
  onTogglePanel: () => void;
  onArchive: () => void;
}) {
  const { t } = useTranslation("shell");
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>('[role^="menuitem"]')?.focus();
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const choose = (action: () => void) => {
    setOpen(false);
    buttonRef.current?.focus();
    action();
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      buttonRef.current?.focus();
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = [
      ...(menu.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]') ??
        []),
    ];
    if (items.length === 0) return;
    e.preventDefault();
    const i = items.indexOf(document.activeElement as HTMLElement);
    const step = e.key === "ArrowDown" ? 1 : -1;
    items[(i + step + items.length) % items.length].focus();
  };

  return (
    <div className="more-wrap" ref={wrap} onKeyDown={onKeyDown}>
      <button
        ref={buttonRef}
        type="button"
        className="icon-btn"
        aria-label={t("more")}
        title={t("more")}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="more" />
      </button>
      {open && (
        <div
          className="pop down more-pop"
          role="menu"
          aria-label={t("moreMenu")}
          ref={menu}
        >
          <button
            type="button"
            className="opt"
            role="menuitemcheckbox"
            aria-checked={panelOpen}
            aria-controls={panelOpen ? PANEL_ID : undefined}
            onClick={() => choose(onTogglePanel)}
          >
            <Icon name="panelRight" />
            <span className="grow">{t("panelItem")}</span>
          </button>
          <button
            type="button"
            className="opt"
            role="menuitem"
            onClick={() => choose(onArchive)}
          >
            <Icon name="archive" />
            <span className="grow">{t("archive")}</span>
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
  onSeen,
  onUpdated,
  onArchived,
  nav,
  hidden,
  catalog,
}: {
  // catalog names the session's model and offers the models and modes to
  // change to; without it the model id shows, read-only.
  catalog?: AgentCatalog;
  // onSeen is told when the server took a seen mark for the session.
  onSeen?: () => void;
  // onUpdated gets the fields of an accepted rename, model or mode change.
  onUpdated?: (fields: SessionPatch) => void;
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
    sinceSeq: session.lastSeq,
  });
  const folded = useMemo(() => foldEvents(stream), [stream]);
  const status = down ? session.status : (folded.status ?? session.status);
  useMarkSeen({
    sessionId: session.id,
    // With the sandbox down the stream delivers nothing: the row's mirrored
    // last seq is all there is to see (a turn cut off by the stop).
    lastSeq: down
      ? Math.max(stream.lastSeq, session.lastSeq ?? 0)
      : stream.lastSeq,
    unseen: session.unseen,
    shown: !hidden,
    onSeen,
  });
  const composerInput = useRef<HTMLTextAreaElement>(null);
  // A rename under way shows at once; a refusal puts the old title back.
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameError, setRenameError] = useState<ErrorCode | null>(null);
  const rename = (title: string) => {
    setRenaming(title);
    setRenameError(null);
    updateSession(session.id, { title }).then(
      (s) => {
        setRenaming(null);
        onUpdated?.({ title: s.summary.title });
      },
      (err: unknown) => {
        setRenaming(null);
        setRenameError(errorCode(err));
      },
    );
  };
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

  // The review panel and the session's worktrees it needs (repoIds). rev
  // asks for both again: the refresh button, a turn's end, the sandbox
  // coming up. The panel reads nothing while closed.
  const [panelOpen, setPanelOpen] = useState(false);
  const [diffRepo, setDiffRepo] = useState<string | undefined>();
  const [rev, setRev] = useState(0);
  const refresh = useCallback(() => setRev((r) => r + 1), []);
  const turnOpen = folded.turnOpen;
  const wasOpen = useRef(turnOpen);
  useEffect(() => {
    if (wasOpen.current && !turnOpen) refresh();
    wasOpen.current = turnOpen;
  }, [turnOpen, refresh]);
  const warm = sandbox?.state === "warm";
  const wasWarm = useRef(warm);
  useEffect(() => {
    if (warm && !wasWarm.current) refresh();
    wasWarm.current = warm;
  }, [warm, refresh]);
  const detail = useSessionDetail(session.id, rev);
  const overlay = useMediaQuery(OVERLAY);
  const narrow = useMediaQuery(NARROW);
  const compact = useMediaQuery(COMPACT);
  // The panel's toggle, or on phones the menu holding it: the focus goes back
  // there when the panel closes.
  const panelToggle = useRef<HTMLButtonElement>(null);
  const togglePanel = () => {
    if (!panelOpen) refresh();
    setPanelOpen((o) => !o);
  };
  const toggleAsking = () => {
    setAsking((a) => !a);
    setFailed(null);
  };
  const openRepo = (repoId: string) => {
    setDiffRepo(repoId);
    if (!panelOpen) refresh();
    setPanelOpen(true);
  };
  const closePanel = () => {
    setPanelOpen(false);
    panelToggle.current?.focus();
  };
  const repos: RepoEntry[] = detail.worktrees
    ? detail.worktrees.map((w) => ({
        repoId: w.repoId,
        name: w.repo,
        added: w.added,
        removed: w.removed,
        path: w.path,
      }))
    : session.repos.map((name) => ({ name }));

  return (
    <section
      className={
        panelOpen ? "view session-view panel-open" : "view session-view"
      }
      aria-label={t("session")}
      hidden={hidden}
    >
      <div className="view-col">
        <Header
          project={projectName}
          title={renaming ?? session.title}
          onRename={renaming === null ? rename : undefined}
          branch={session.branch}
          sandbox={sandbox?.state}
          sandboxBusy={sandboxBusy(sandbox)}
          repos={repos}
          onOpenRepo={openRepo}
          actions={
            <>
              <TerminalToggle
                pressed={terminalOpen}
                onToggle={() => setTerminalOpen((o) => !o)}
              />
              {compact ? (
                <MoreMenu
                  buttonRef={panelToggle}
                  panelOpen={panelOpen}
                  onTogglePanel={togglePanel}
                  onArchive={toggleAsking}
                />
              ) : (
                <>
                  <button
                    ref={panelToggle}
                    type="button"
                    className="icon-btn"
                    aria-label={t("togglePanel")}
                    title={t("togglePanel")}
                    aria-pressed={panelOpen}
                    aria-controls={panelOpen ? PANEL_ID : undefined}
                    onClick={togglePanel}
                  >
                    <Icon name="panelRight" />
                  </button>
                  <button
                    type="button"
                    className="icon-btn"
                    aria-label={t("archive")}
                    title={t("archive")}
                    aria-expanded={asking}
                    onClick={toggleAsking}
                  >
                    <Icon name="archive" />
                  </button>
                </>
              )}
              {nav}
            </>
          }
        />
        {renameError && (
          <div className="confirm hdr-confirm" role="alert">
            {t("renameFailed")} {t(renameError, { ns: "errors" })}
            <div className="row-inline">
              <button
                type="button"
                className="btn sm ghost"
                onClick={() => setRenameError(null)}
              >
                {t("dismiss")}
              </button>
            </div>
          </div>
        )}
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
              key={`${downState}:${sandboxBusy(sandbox)}`}
              projectId={session.projectId}
              state={downState}
              busy={sandboxBusy(sandbox)}
            />
          )}
          <Timeline
            sessionId={session.id}
            folded={folded}
            working={status === "working"}
            provider={session.provider}
            modes={catalog?.modes}
            modeCap={session.modeCap}
            mode={session.mode}
            onAnswerInComposer={() => composerInput.current?.focus()}
          />
          <Composer
            sessionId={session.id}
            provider={session.provider}
            model={session.model}
            effort={session.effort}
            mode={session.mode}
            modeCap={session.modeCap}
            catalog={catalog}
            projectName={projectName}
            queue={folded.queue}
            questionId={folded.pendingQuestion?.request.requestId}
            inputRef={composerInput}
            onUpdated={onUpdated}
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
      </div>
      {panelOpen && (
        <ReviewPanel
          sessionId={session.id}
          projectId={session.projectId}
          branch={session.branch}
          worktrees={detail.worktrees}
          worktreesError={detail.worktrees ? undefined : detail.error}
          repoId={diffRepo}
          onRepo={setDiffRepo}
          rev={rev}
          onRefresh={refresh}
          onClose={closePanel}
          sandboxState={sandbox?.state}
          sandboxBusy={sandboxBusy(sandbox)}
          overlay={overlay}
          narrow={narrow}
        />
      )}
    </section>
  );
}

// OVERLAY: up to this width the review panel lies over the session.
const OVERLAY = "(max-width: 1180px)";
// COMPACT: phones, where the diff panel toggle and Archive share one menu
// (shell.css hides the sandbox and repos chips at the same width).
const COMPACT = "(max-width: 420px)";

// useSessionDetail reads the session's worktrees (getSession), again when
// rev changes; a failure leaves the last ones (or none) and its code.
function useSessionDetail(
  sessionId: string,
  rev: number,
): { worktrees?: Worktree[]; error?: ErrorCode } {
  const [state, setState] = useState<{
    worktrees?: Worktree[];
    error?: ErrorCode;
  }>({});
  useEffect(() => {
    let live = true;
    getSession(sessionId).then(
      (s) => {
        if (live) setState({ worktrees: s.worktrees ?? [] });
      },
      (err: unknown) => {
        if (live) setState((p) => ({ ...p, error: errorCode(err) }));
      },
    );
    return () => {
      live = false;
    };
  }, [sessionId, rev]);
  return state;
}
