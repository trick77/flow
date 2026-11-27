import {
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import { createSession, errorCode } from "../api";
import type {
  AgentCatalog,
  ClaudeAuth,
  ErrorCode,
  PermissionMode,
  Project,
  Session,
} from "../api/types.gen";
import { AgentPill, useAgentName } from "../shell/AgentPill";
import { projectBadge } from "../shell/badge";
import { Icon } from "../shell/Icon";
import {
  defaultAuth,
  defaultChoice,
  defaultMode,
  defaultProject,
  findModel,
  useCatalog,
} from "./catalog";
import "./composer.css";
import { authKeys, modeKeys } from "./modes";
import { Picker, type PickerOption } from "./Picker";
import { loadPrefs, savePrefs } from "./prefs";

// NewSession starts a session: the project, its repositories (all picked at
// first), the model, the permission mode and the first message. The models
// and modes come from the agent catalog; without an agent that can run it
// points to Settings instead. The last project, model and mode used in this
// browser are the defaults next time. onCreated gets the session the moment
// the server has made it.
export function NewSession({
  projects,
  currentProjectId,
  nav,
  onCreated,
  onOpenSettings,
  onOpenProjects,
}: {
  projects: readonly Project[];
  // The project of the session on screen, the default after the remembered.
  currentProjectId?: string;
  nav?: ReactNode;
  onCreated: (session: Session) => void;
  onOpenSettings: () => void;
  onOpenProjects: () => void;
}) {
  const { t } = useTranslation("composer");
  const agentName = useAgentName();
  const [catalogState, retry] = useCatalog(true);
  const [prefs] = useState(() => loadPrefs());
  const [projectId, setProjectId] = useState<string>();
  // Repositories left out, per project: a project switched to starts with
  // all of its repositories.
  const [left, setLeft] = useState<Record<string, string[]>>({});
  const [model, setModel] = useState<string>();
  const [mode, setMode] = useState<PermissionMode>();
  const [auth, setAuth] = useState<ClaudeAuth>();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  const draftId = useRef<string | null>(null);
  const inputId = useId();

  const project =
    projects.find((p) => p.id === projectId) ??
    defaultProject(projects, prefs, currentProjectId);
  const catalog: AgentCatalog | undefined =
    catalogState.state === "ready" ? catalogState.catalog : undefined;
  const choice = catalog
    ? (findModel(catalog, model) ?? defaultChoice(catalog, prefs))
    : undefined;
  const modes = (catalog?.modes ?? []).filter((m) => m in modeKeys);
  const chosenMode =
    mode ?? (catalog ? defaultMode({ ...catalog, modes }, prefs) : undefined);
  const auths = choice?.provider.claudeAuths ?? [];
  const chosenAuth =
    choice?.provider.provider === "claude"
      ? auth && auths.includes(auth)
        ? auth
        : defaultAuth(choice.provider)
      : undefined;
  const leftOut = project ? (left[project.id] ?? []) : [];
  const repoIds = project
    ? project.repos.filter((r) => !leftOut.includes(r.id)).map((r) => r.id)
    : [];

  const toggleRepo = (id: string) => {
    if (!project) return;
    setLeft((l) => ({
      ...l,
      [project.id]: leftOut.includes(id)
        ? leftOut.filter((x) => x !== id)
        : [...leftOut, id],
    }));
  };

  const ready =
    !!project && !!choice && !!chosenMode && repoIds.length > 0 && !busy;

  const start = () => {
    const body = text.trim();
    if (!ready || !project || !choice || !chosenMode || body === "") return;
    setBusy(true);
    setError(null);
    draftId.current ??= crypto.randomUUID();
    createSession({
      projectId: project.id,
      repoIds,
      provider: choice.provider.provider,
      claudeAuth: chosenAuth,
      model: choice.model.id,
      mode: chosenMode,
      message: { clientId: draftId.current, text: body },
    }).then(
      (session) => {
        savePrefs({
          projectId: project.id,
          model: choice.model.id,
          mode: chosenMode,
        });
        onCreated(session);
      },
      (err: unknown) => {
        setError(errorCode(err));
        setBusy(false);
      },
    );
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    start();
  };
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      start();
    }
  };

  const projectOptions: PickerOption<string>[] = projects.map((p) => ({
    value: p.id,
    label: p.name,
    lead: (
      <span className="mono-badge" aria-hidden="true">
        {projectBadge(p.name)}
      </span>
    ),
  }));
  const modelOptions: PickerOption<string>[] = (
    catalog?.providers ?? []
  ).flatMap((p) =>
    p.models.map((m) => ({
      value: m.id,
      label: m.label,
      description: p.available ? (m.via ?? undefined) : t("agentUnavailable"),
      disabled: !p.available,
      group: agentName(p.provider),
    })),
  );
  const modeOptions: PickerOption<PermissionMode>[] = modes.map((m) => ({
    value: m,
    label: t(modeKeys[m].label),
    description: t(modeKeys[m].hint),
    warn: m === "full_access",
  }));
  const authOptions: PickerOption<ClaudeAuth>[] = auths
    .filter((a) => a in authKeys)
    .map((a) => ({ value: a, label: t(authKeys[a]) }));

  let dock: ReactNode;
  if (!project) {
    dock = (
      <div className="start-notice" role="status">
        <p>{t("noProject")}</p>
        <button type="button" className="btn sm" onClick={onOpenProjects}>
          {t("openProjects")}
        </button>
      </div>
    );
  } else if (catalogState.state === "loading") {
    dock = (
      <p className="start-notice" role="status">
        {t("catalogLoading")}
      </p>
    );
  } else if (catalogState.state === "failed") {
    dock = (
      <div className="start-notice" role="alert">
        <p>
          {t("catalogFailed")} {t(catalogState.error, { ns: "errors" })}
        </p>
        <button type="button" className="btn sm" onClick={retry}>
          {t("retry")}
        </button>
      </div>
    );
  } else if (!choice) {
    dock = (
      <div className="start-notice" role="alert">
        <p>{t("noAgent")}</p>
        <button type="button" className="btn sm" onClick={onOpenSettings}>
          {t("openSettings")}
        </button>
      </div>
    );
  } else {
    dock = (
      <>
        <form
          className="composer"
          onSubmit={onSubmit}
          aria-label={t("newTitle")}
        >
          <label className="sr" htmlFor={inputId}>
            {t("startPlaceholder")}
          </label>
          <textarea
            id={inputId}
            rows={2}
            placeholder={t("startPlaceholder")}
            value={text}
            readOnly={busy}
            aria-busy={busy}
            autoFocus
            onChange={(e) => {
              draftId.current = null;
              setText(e.target.value);
            }}
            onKeyDown={onKeyDown}
          />
          <div className="comp-bar">
            <Picker
              label={t("modelOf", {
                agent: agentName(choice.provider.provider),
                model: choice.model.label,
              })}
              value={choice.model.id}
              options={modelOptions}
              onChange={setModel}
            >
              <AgentPill provider={choice.provider.provider} />
              <span>
                <span className="long">
                  {t("agentPrefix", {
                    agent: agentName(choice.provider.provider),
                  })}
                </span>
                {choice.model.label}
              </span>
            </Picker>
            {chosenMode && (
              <Picker
                label={t("modeOf", { mode: t(modeKeys[chosenMode].label) })}
                heading={t("mode")}
                value={chosenMode}
                options={modeOptions}
                onChange={setMode}
              >
                <Icon name="shield" />
                <span>{t(modeKeys[chosenMode].label)}</span>
              </Picker>
            )}
            {authOptions.length > 1 && chosenAuth && (
              <Picker
                label={t("claudeAuthOf", { auth: t(authKeys[chosenAuth]) })}
                heading={t("claudeAuth")}
                value={chosenAuth}
                options={authOptions}
                onChange={setAuth}
              >
                <Icon name="key" />
                <span>{t(authKeys[chosenAuth])}</span>
              </Picker>
            )}
            <span className="grow" />
            <button
              className="send"
              type="submit"
              aria-label={busy ? t("starting") : t("start")}
              disabled={!ready || text.trim() === ""}
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
        <p className="comp-note">{t("runsIn", { project: project.name })}</p>
      </>
    );
  }

  return (
    <section className="view" aria-label={t("newTitle")}>
      <header className="hdr">
        <div className="crumb">
          {project && projects.length > 0 && (
            <>
              <Picker
                label={t("projectOf", { name: project.name })}
                heading={t("project")}
                value={project.id}
                options={projectOptions}
                onChange={(id) => {
                  setProjectId(id);
                  setError(null);
                }}
                className="proj crumb-pick"
                placement="down"
              >
                <span className="mono-badge" aria-hidden="true">
                  {projectBadge(project.name)}
                </span>
                <span>{project.name}</span>
              </Picker>
              <span className="sep" aria-hidden="true">
                {t("crumbSeparator", { ns: "shell" })}
              </span>
            </>
          )}
          <h2 className="ttl">{t("newTitle")}</h2>
        </div>
        <span className="hdr-spacer" />
        {nav}
      </header>
      <div className="center-body">
        <div className="timeline">
          <div className="tl">
            {project && (
              <div className="empty-start">
                <p className="big">
                  {t("startQuestion", { project: project.name })}
                </p>
                <p>{t("startHint")}</p>
                {project.repos.length > 0 && (
                  <div className="rpick" role="group" aria-label={t("repos")}>
                    {project.repos.map((r) => (
                      <label className="rchip" key={r.id}>
                        <input
                          type="checkbox"
                          checked={!leftOut.includes(r.id)}
                          disabled={busy}
                          onChange={() => toggleRepo(r.id)}
                        />
                        <span className="box" aria-hidden="true">
                          <Icon name="check" />
                        </span>
                        <span>{r.fullName}</span>
                      </label>
                    ))}
                  </div>
                )}
                {repoIds.length === 0 && (
                  <p className="rpick-note" role="alert">
                    {t("reposNone")}
                  </p>
                )}
              </div>
            )}
          </div>
        </div>
        <div className="dock">
          <div className="dock-in">{dock}</div>
        </div>
      </div>
    </section>
  );
}
