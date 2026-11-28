import { useCallback, useEffect, useId, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";

import { ApiError, createProject, errorCode, getConnections } from "../api";
import type { ErrorCode, Project } from "../api/types.gen";
import { Icon } from "../shell";
import { DeviceOutcome, DevicePanel, flowEnded } from "./DevicePanel";
import { repoKey, repoName, rowState } from "./github";
import {
  RepoByName,
  RepoChips,
  RepoPicker,
  type ChosenRepo,
} from "./RepoPicker";
import { useDeviceFlow } from "./useDeviceFlow";

// GitHubSource is what the form can offer for choosing repositories.
// offer: GitHub can be connected here (expired: it was, and ran out).
// unknown: the connections could not be read; adding by name still works.
type GitHubSource =
  | { kind: "loading" }
  | { kind: "connected" }
  | { kind: "offer"; expired: boolean }
  | { kind: "unavailable" }
  | { kind: "unknown" };

type FieldKey = "invalidName" | "nameTaken" | "invalidRepos" | "notFoundPublic";
type Failure = { field: "name" | "repos"; key: FieldKey } | { code: ErrorCode };

// fieldError is the sentence for a refusal of the create form: the field's
// own where the API names one, else the code's. github_not_connected on a
// repository means it was not found as a public one: maybe it is not public.
function fieldError(err: unknown): Failure {
  if (err instanceof ApiError && err.field === "name") {
    return {
      field: "name",
      key: err.code === "conflict" ? "nameTaken" : "invalidName",
    };
  }
  if (err instanceof ApiError && err.field === "repos") {
    if (err.code === "invalid_request") {
      return { field: "repos", key: "invalidRepos" };
    }
    if (err.code === "github_not_connected") {
      return { field: "repos", key: "notFoundPublic" };
    }
  }
  return { code: errorCode(err) };
}

// useGitHubSource reads the GitHub connection once the form opens.
function useGitHubSource() {
  const [source, setSource] = useState<GitHubSource>({ kind: "loading" });
  useEffect(() => {
    let live = true;
    getConnections().then(
      (c) => {
        if (!live) return;
        const s = rowState(c.github);
        setSource(
          s === "unavailable"
            ? { kind: "unavailable" }
            : s === "connected"
              ? { kind: "connected" }
              : { kind: "offer", expired: s === "reconnect" },
        );
      },
      (err: unknown) => {
        if (!live) return;
        setSource(
          err instanceof ApiError && err.code === "not_implemented"
            ? { kind: "unavailable" }
            : { kind: "unknown" },
        );
      },
    );
    return () => {
      live = false;
    };
  }, []);
  return [source, setSource] as const;
}

// AddProject is the create form: the repositories (picked from GitHub when
// it is connected, else connected right here, and always addable by name)
// and a name that follows the first repository until the owner types one.
export function AddProject({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (p: Project) => void;
}) {
  const { t } = useTranslation("projects");
  const uid = useId();
  const [source, setSource] = useGitHubSource();
  const [repos, setRepos] = useState<ChosenRepo[]>([]);
  const [typedName, setTypedName] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const onConnected = useCallback(
    () => setSource({ kind: "connected" }),
    [setSource],
  );
  const device = useDeviceFlow(onConnected);
  const flow = device.state;

  useEffect(() => {
    if (flow.phase === "unavailable") setSource({ kind: "unavailable" });
  }, [flow.phase, setSource]);

  // typedName null: the name follows the first repository.
  // A repository name may be longer than a project's (64, as the server
  // counts): the followed name is cut there.
  const name =
    typedName ?? (repos[0] ? repoName(repos[0].fullName).slice(0, 64) : "");
  const add = (r: ChosenRepo) => {
    setRepos((rs) =>
      rs.some((x) => repoKey(x.fullName) === repoKey(r.fullName))
        ? rs
        : [...rs, r],
    );
    if (failure && "field" in failure && failure.field === "repos") {
      setFailure(undefined);
    }
  };
  const remove = (fullName: string) =>
    setRepos((rs) =>
      rs.filter((x) => repoKey(x.fullName) !== repoKey(fullName)),
    );

  const missing =
    repos.length === 0 ? "needRepos" : name.trim() === "" ? "needName" : null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (missing || busy) return;
    setBusy(true);
    setFailure(undefined);
    createProject({
      name: name.trim(),
      repos: repos.map((r) => r.fullName),
    }).then(onCreated, (err: unknown) => {
      const f = fieldError(err);
      setFailure(f);
      setBusy(false);
      // The server found no connection after all: offer to connect.
      if (
        "key" in f &&
        f.key === "notFoundPublic" &&
        source.kind === "connected"
      ) {
        setSource({ kind: "offer", expired: true });
      }
    });
  };

  const fieldFailure = (field: "name" | "repos") =>
    failure && "field" in failure && failure.field === field
      ? t(failure.key)
      : null;
  const nameErr = fieldFailure("name");
  const reposErr = fieldFailure("repos");
  const waiting = flow.phase === "waiting" ? flow.flow : null;
  const starting = flow.phase === "starting";
  const reposLabel = `${uid}-repos`;

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
        <div
          className="field"
          role="group"
          aria-labelledby={reposLabel}
          aria-describedby={reposErr ? `${uid}-repos-err` : undefined}
        >
          <span className="label" id={reposLabel}>
            {t("repos")}
          </span>
          {source.kind === "connected" ? (
            <>
              <DeviceOutcome flow={flow} />
              <RepoPicker
                chosen={repos}
                onAdd={add}
                onRemove={remove}
                onDisconnected={() =>
                  setSource({ kind: "offer", expired: true })
                }
                labelId={reposLabel}
                invalid={reposErr !== null}
              />
            </>
          ) : (
            <RepoChips chosen={repos} onRemove={remove} />
          )}
          {source.kind === "loading" && (
            <p className="hint">{t("repoLoading")}</p>
          )}
          {source.kind === "offer" && (
            <div className="addp-gh">
              <p>{source.expired ? t("ghExpired") : t("ghOffer")}</p>
              {!waiting && (
                <div className="row-inline">
                  <button
                    type="button"
                    className="btn sm primary"
                    disabled={starting}
                    onClick={device.start}
                  >
                    <Icon name="repo" />
                    {starting
                      ? t("ghStarting")
                      : flowEnded(flow)
                        ? t("ghTryAgain")
                        : source.expired
                          ? t("ghConnectAgain")
                          : t("ghConnect")}
                  </button>
                </div>
              )}
              {waiting && (
                <DevicePanel
                  flow={waiting}
                  remainingMs={device.remainingMs}
                  paused={device.paused}
                  onCancel={device.cancel}
                />
              )}
              <DeviceOutcome flow={flow} />
            </div>
          )}
          {source.kind === "unavailable" && (
            <p className="hint">{t("ghUnavailable")}</p>
          )}
          {reposErr && (
            <span className="err" id={`${uid}-repos-err`} role="alert">
              {reposErr}
            </span>
          )}
        </div>
        {source.kind !== "connected" && source.kind !== "loading" && (
          <RepoByName
            chosen={repos}
            onAdd={add}
            label={source.kind === "offer" ? t("byNameOr") : t("byName")}
          />
        )}
        <div className="field">
          <label htmlFor={`${uid}-name`}>{t("name")}</label>
          <input
            id={`${uid}-name`}
            className="input"
            type="text"
            autoComplete="off"
            placeholder={t("namePlaceholder")}
            maxLength={64}
            value={name}
            aria-invalid={nameErr !== null || undefined}
            aria-describedby={`${uid}-name-hint`}
            onChange={(e) =>
              // Clearing the field lets the name follow again.
              setTypedName(e.target.value === "" ? null : e.target.value)
            }
          />
          <span className="hint" id={`${uid}-name-hint`}>
            {t("nameHint")}
          </span>
          {nameErr && (
            <span className="err" role="alert">
              {nameErr}
            </span>
          )}
        </div>
        {failure && "code" in failure && (
          <p className="page-error" role="alert">
            {t(failure.code, { ns: "errors" })}
          </p>
        )}
        <div className="row-inline">
          <button
            type="submit"
            className="btn primary"
            disabled={busy || missing !== null}
            aria-describedby={missing ? `${uid}-missing` : undefined}
          >
            {busy ? t("creating") : t("create")}
          </button>
          <button type="button" className="btn ghost" onClick={onClose}>
            {t("cancel")}
          </button>
          {missing && (
            <span className="form-status" id={`${uid}-missing`}>
              {t(missing)}
            </span>
          )}
        </div>
      </div>
    </form>
  );
}
