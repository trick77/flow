import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteProject,
  errorCode,
  sandboxAction,
  sandboxBusy,
  sandboxStale,
  sandboxStartable,
  type SandboxAction,
} from "../api";
import type {
  AppEvent,
  ErrorCode,
  Project,
  Sandbox,
  SandboxState,
} from "../api/types.gen";
import { formatDate, formatDateTime, formatNumber } from "../i18n/format";
import { Icon, projectBadge } from "../shell";
import { AddProject } from "./AddProject";
import { Page } from "./Page";
import { SetupLog } from "./SetupLog";

// ProjectsPage lists the projects with their sandboxes and creates, starts,
// stops, rebuilds and deletes them. The list is the app's (kept by the app
// event stream). A creation's and a deletion's answer go to onEvent at once;
// a sandbox's state comes from the stream alone (an answer may be older than
// an event the stream has shown already).
export function ProjectsPage({
  projects,
  openSessions = {},
  loaded,
  error,
  onEvent,
  nav,
  onBack,
}: {
  projects: Project[];
  // openSessions counts the sessions not archived per project id.
  openSessions?: Record<string, number>;
  loaded: boolean;
  error: ErrorCode | null;
  onEvent: (e: AppEvent) => void;
  nav: ReactNode;
  onBack?: () => void;
}) {
  const { t } = useTranslation("projects");
  const [adding, setAdding] = useState(false);
  return (
    <Page
      title={t("title")}
      lead={t("lead")}
      nav={nav}
      onBack={onBack}
      action={
        !adding && (
          <button
            type="button"
            className="btn primary"
            onClick={() => setAdding(true)}
          >
            <Icon name="plus" />
            {t("add")}
          </button>
        )
      }
    >
      {adding && (
        <AddProject
          onClose={() => setAdding(false)}
          onCreated={(p) => {
            onEvent({ kind: "project.updated", project: p, projectId: p.id });
            setAdding(false);
          }}
        />
      )}
      {error ? (
        <p className="page-error" role="alert">
          {t("loadFailed")} {t(error, { ns: "errors" })}
        </p>
      ) : loaded && projects.length === 0 && !adding ? (
        <div className="page-note">
          <p>{t("empty")}</p>
          <p>{t("emptyHint")}</p>
        </div>
      ) : (
        <div className="pgrid">
          {projects.map((p) => (
            <ProjectCard
              key={p.id}
              project={p}
              open={openSessions[p.id] ?? 0}
              onEvent={onEvent}
            />
          ))}
        </div>
      )}
    </Page>
  );
}

const stateKey: Record<
  SandboxState,
  | "sandboxNone"
  | "sandboxSetup"
  | "sandboxWarm"
  | "sandboxStopping"
  | "sandboxStopped"
  | "sandboxFailed"
  | "sandboxDeleting"
> = {
  none: "sandboxNone",
  setup: "sandboxSetup",
  warm: "sandboxWarm",
  stopping: "sandboxStopping",
  stopped: "sandboxStopped",
  failed: "sandboxFailed",
  deleting: "sandboxDeleting",
};

type Label = {
  key:
    | (typeof stateKey)[SandboxState]
    | "busySetup"
    | "busyStopping"
    | "busyDeleting"
    | "busyWorking"
    | "staleSetup"
    | "staleStop";
  tone: string;
};

// stateLabel is the card's state line: what runs now while the server is
// busy with the sandbox; a setup or stop left behind in words of its own.
function stateLabel(sb: Sandbox): Label {
  if (sandboxBusy(sb)) {
    const key =
      sb.state === "stopping"
        ? "busyStopping"
        : sb.state === "deleting"
          ? "busyDeleting"
          : sb.state === "warm"
            ? "busyWorking"
            : "busySetup";
    return { key, tone: "busy" };
  }
  if (sandboxStale(sb)) {
    return {
      key: sb.state === "setup" ? "staleSetup" : "staleStop",
      tone: "failed",
    };
  }
  return { key: stateKey[sb.state], tone: sb.state };
}

type Pending = "rebuild" | "delete" | null;

// ProjectCard is one project: its repositories, its sandbox's state and what
// can be done with it now. Rebuild and delete ask first.
function ProjectCard({
  project,
  open,
  onEvent,
}: {
  project: Project;
  open: number;
  onEvent: (e: AppEvent) => void;
}) {
  const { t } = useTranslation("projects");
  // sending: this card's own request is under way.
  const [sending, setSending] = useState(false);
  const [confirm, setConfirm] = useState<Pending>(null);
  const [failed, setFailed] = useState<ErrorCode | null>(null);
  const sb = project.sandbox;
  const state = sb.state;
  // While the server runs an operation on the sandbox, start, stop, rebuild
  // and delete would wait or be refused with sandbox_busy: none is offered.
  const busy = sandboxBusy(sb);
  // A confirmation open when the sandbox turns busy is dropped, not kept for
  // when it ends (the owner asked about a sandbox that has changed since).
  const [wasBusy, setWasBusy] = useState(busy);
  if (busy !== wasBusy) {
    setWasBusy(busy);
    if (busy) setConfirm(null);
  }
  // A failed setup script: its log says why.
  const setupFailed = sb.errorCode === "sandbox_setup_failed";
  const [showLog, setShowLog] = useState(false);
  // A setup or stopping state left behind (nothing runs) is offered start,
  // rebuild and delete. Stop never: the sandbox is not known to run. A
  // project left in deleting (its rows failed to go) offers the delete again.
  const startable = sandboxStartable(sb);
  const label = stateLabel(sb);

  const run = (call: () => Promise<void>) => {
    setSending(true);
    setFailed(null);
    setConfirm(null);
    call().then(
      () => setSending(false),
      (err: unknown) => {
        setFailed(errorCode(err));
        setSending(false);
      },
    );
  };
  const act = (action: SandboxAction) =>
    run(() => sandboxAction(project.id, action).then(() => undefined));
  const remove = () =>
    run(() =>
      deleteProject(project.id).then(() =>
        onEvent({ kind: "project.removed", projectId: project.id }),
      ),
    );

  const badge = projectBadge(project.name);
  return (
    <article className="pcard" aria-label={project.name}>
      <div className="pcard-h">
        <span className="pbadge" aria-hidden="true">
          {badge}
        </span>
        <div className="nm">
          <h2 className="name">{project.name}</h2>
          <div className="repo">
            {t("repoCount", {
              count: project.repos.length,
              n: formatNumber(project.repos.length),
            })}
          </div>
        </div>
        <span className={`sbx ${label.tone}`} role="status">
          {t(label.key)}
        </span>
      </div>
      <ul className="prepos" aria-label={t("reposIn", { name: project.name })}>
        {project.repos.map((r) => (
          <li key={r.id} className="chip">
            <Icon name="repo" />
            <span className="mono">{r.fullName}</span>
          </li>
        ))}
      </ul>
      <dl>
        <dt>{t("created")}</dt>
        <dd>{formatDate(new Date(project.createdAt))}</dd>
        <dt>{t("lastUsed")}</dt>
        <dd>
          {sb.lastUsedAt ? formatDateTime(new Date(sb.lastUsedAt)) : t("never")}
        </dd>
        {sb.flowdVersion && (
          <>
            <dt>{t("flowd")}</dt>
            <dd className="mono" title={sb.flowdVersion}>
              {sb.flowdVersion}
            </dd>
          </>
        )}
      </dl>
      {sb.errorCode && state === "failed" && (
        <p className="pcard-err">{t(sb.errorCode, { ns: "errors" })}</p>
      )}
      {setupFailed && showLog && <SetupLog projectId={project.id} />}
      {failed && (
        <p className="pcard-err" role="alert">
          {failed === "project_has_sessions" && open > 0
            ? t("openSessions", { count: open, n: formatNumber(open) })
            : t(failed, { ns: "errors" })}
        </p>
      )}
      {busy && <p className="pcard-busy">{t("busyNote")}</p>}
      {confirm && !busy && (
        <div className="confirm" role="alertdialog" aria-label={project.name}>
          {confirm === "rebuild"
            ? t("confirmRebuild", { name: project.name })
            : t("confirmDelete", { name: project.name })}
          <div className="row-inline">
            <button
              type="button"
              className="btn sm danger"
              onClick={confirm === "rebuild" ? () => act("rebuild") : remove}
            >
              {confirm === "rebuild"
                ? t("confirmRebuildAction")
                : t("confirmDeleteAction")}
            </button>
            <button
              type="button"
              className="btn sm ghost"
              onClick={() => setConfirm(null)}
            >
              {t("cancel")}
            </button>
          </div>
        </div>
      )}
      {!confirm && !busy && (
        <div className="pcard-foot">
          {startable && (
            <button
              type="button"
              className="btn sm"
              disabled={sending}
              onClick={() => act("start")}
            >
              {t("start")}
            </button>
          )}
          {state === "warm" && (
            <button
              type="button"
              className="btn sm"
              disabled={sending}
              onClick={() => act("stop")}
            >
              {t("stop")}
            </button>
          )}
          {setupFailed && (
            <button
              type="button"
              className="btn sm ghost"
              aria-expanded={showLog}
              onClick={() => setShowLog((s) => !s)}
            >
              <Icon name="list" />
              {showLog ? t("hideSetupLog") : t("setupLog")}
            </button>
          )}
          {state !== "deleting" && (
            <button
              type="button"
              className="btn sm ghost"
              disabled={sending}
              onClick={() => setConfirm("rebuild")}
            >
              <Icon name="reload" />
              {t("rebuild")}
            </button>
          )}
          <button
            type="button"
            className="btn sm ghost"
            disabled={sending}
            onClick={() => setConfirm("delete")}
          >
            <Icon name="trash" />
            {t("delete")}
          </button>
        </div>
      )}
    </article>
  );
}
