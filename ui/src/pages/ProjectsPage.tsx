import { useState, type FormEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  ApiError,
  createProject,
  deleteProject,
  errorCode,
  sandboxAction,
  type SandboxAction,
} from "../api";
import type {
  AppEvent,
  ErrorCode,
  Project,
  SandboxState,
} from "../api/types.gen";
import { formatDate, formatDateTime } from "../i18n/format";
import { Icon, projectBadge } from "../shell";
import { Page } from "./Page";

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

// fieldError is the sentence for a refusal of the create form: the field's
// own where the API names one, else the code's.
function fieldError(err: unknown): { field?: string; key: string } {
  if (err instanceof ApiError && err.field === "name") {
    return {
      field: "name",
      key: err.code === "conflict" ? "nameTaken" : "invalidName",
    };
  }
  if (err instanceof ApiError && err.field === "repos") {
    return { field: "repos", key: "invalidRepos" };
  }
  return { key: errorCode(err) };
}

// AddProject is the create form: a name and the repositories, one per line.
function AddProject({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (p: Project) => void;
}) {
  const { t } = useTranslation("projects");
  const [name, setName] = useState("");
  const [repos, setRepos] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ field?: string; key: string }>();

  const list = repos
    .split("\n")
    .map((r) => r.trim())
    .filter((r) => r !== "");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    createProject({ name: name.trim(), repos: list }).then(onCreated, (err) => {
      setFailure(fieldError(err));
      setBusy(false);
    });
  };
  const message = (key: string) =>
    failure?.field
      ? t(key as "invalidName" | "nameTaken" | "invalidRepos")
      : t(key as ErrorCode, { ns: "errors" });

  return (
    <form className="addp" onSubmit={submit} aria-label={t("createTitle")}>
      <div className="addp-h">
        <Icon name="repo" className="file-ic" />
        <div className="grow">
          <div className="card-title">{t("createTitle")}</div>
        </div>
        <button
          type="button"
          className="icon-btn"
          aria-label={t("close")}
          onClick={onClose}
        >
          <Icon name="x" />
        </button>
      </div>
      <div className="addp-b">
        <div className="field">
          <label htmlFor="project-name">{t("name")}</label>
          <input
            id="project-name"
            className="input"
            type="text"
            autoComplete="off"
            placeholder={t("namePlaceholder")}
            maxLength={64}
            value={name}
            aria-invalid={failure?.field === "name" || undefined}
            onChange={(e) => setName(e.target.value)}
          />
          {failure?.field === "name" && (
            <span className="err" role="alert">
              {message(failure.key)}
            </span>
          )}
        </div>
        <div className="field">
          <label htmlFor="project-repos">{t("repos")}</label>
          <textarea
            id="project-repos"
            className="input"
            rows={3}
            autoComplete="off"
            spellCheck={false}
            placeholder={t("reposPlaceholder")}
            value={repos}
            aria-invalid={failure?.field === "repos" || undefined}
            aria-describedby="project-repos-hint"
            onChange={(e) => setRepos(e.target.value)}
          />
          <span className="hint" id="project-repos-hint">
            {t("reposHint")}
          </span>
          {failure?.field === "repos" && (
            <span className="err" role="alert">
              {message(failure.key)}
            </span>
          )}
        </div>
        {failure && !failure.field && (
          <p className="page-error" role="alert">
            {message(failure.key)}
          </p>
        )}
        <div className="row-inline">
          <button
            type="submit"
            className="btn primary"
            disabled={busy || name.trim() === "" || list.length === 0}
          >
            {busy ? t("creating") : t("create")}
          </button>
          <button type="button" className="btn ghost" onClick={onClose}>
            {t("cancel")}
          </button>
        </div>
      </div>
    </form>
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
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<Pending>(null);
  const [failed, setFailed] = useState<ErrorCode | null>(null);
  const sb = project.sandbox;
  const state = sb.state;
  // setup and stopping are offered start, rebuild and delete as well: such a
  // state can outlive its operation, and while one really runs the server
  // answers sandbox_busy. Stop is not: it would wait for a whole bring-up. A
  // project left in deleting (its rows failed to go) offers the delete again.
  const startable =
    state === "none" ||
    state === "stopped" ||
    state === "failed" ||
    state === "setup" ||
    state === "stopping";

  const run = (call: () => Promise<void>) => {
    setBusy(true);
    setFailed(null);
    setConfirm(null);
    call().then(
      () => setBusy(false),
      (err: unknown) => {
        setFailed(errorCode(err));
        setBusy(false);
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
            {t("repoCount", { count: project.repos.length })}
          </div>
        </div>
        <span className={`sbx ${state}`}>{t(stateKey[state])}</span>
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
      {failed && (
        <p className="pcard-err" role="alert">
          {failed === "project_has_sessions" && open > 0
            ? t("openSessions", { count: open })
            : t(failed, { ns: "errors" })}
        </p>
      )}
      {confirm && (
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
      {!confirm && (
        <div className="pcard-foot">
          {startable && (
            <button
              type="button"
              className="btn sm"
              disabled={busy}
              onClick={() => act("start")}
            >
              {t("start")}
            </button>
          )}
          {state === "warm" && (
            <button
              type="button"
              className="btn sm"
              disabled={busy}
              onClick={() => act("stop")}
            >
              {t("stop")}
            </button>
          )}
          {state !== "deleting" && (
            <button
              type="button"
              className="btn sm ghost"
              disabled={busy}
              onClick={() => setConfirm("rebuild")}
            >
              <Icon name="reload" />
              {t("rebuild")}
            </button>
          )}
          <button
            type="button"
            className="btn sm ghost"
            disabled={busy}
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
