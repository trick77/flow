import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import { answerLost, createSession, errorCode } from "../api";
import type {
  AgentCatalog,
  ClaudeAuth,
  Effort,
  ErrorCode,
  PermissionMode,
  Project,
  Session,
} from "../api/types.gen";
import { AgentPill, useAgentName } from "../shell/AgentPill";
import { projectBadge } from "../shell/badge";
import { MenuButton } from "../shell/drawer";
import { Icon } from "../shell/Icon";
import {
  choiceKey,
  defaultAuth,
  defaultChoice,
  defaultEffort,
  defaultMode,
  defaultProject,
  efforts,
  findModel,
  parseChoiceKey,
  refreshCatalog,
  useCatalog,
} from "./catalog";
import "./composer.css";
import { authKeys, effortKeys, modeKeys } from "./modes";
import { Picker, type PickerOption } from "./Picker";
import { loadPrefs, savePrefs } from "./prefs";
import { RepoChoice } from "./RepoChoice";

// From this many projects on, the project menu has a search field and lists
// the most recently used first.
export const PROJECT_SEARCH_FROM = 8;

// NewSession starts a session: the project, its repositories (all picked at
// first), the model, the effort, the permission mode and the first message.
// The models, efforts and modes come from the agent catalog; without an agent
// that can run it points to Settings instead. The project of the session on
// screen is the default, else the one used last in this browser; the last
// model and effort used are the default next time. Every session starts in plan mode (defaultMode), never in
// the one picked last. onCreated gets the session the moment the server has
// made it.
//
// The app keeps it mounted while another view shows (hidden): the draft and
// a start under way survive a look elsewhere.
export function NewSession({
  projects,
  projectsLoaded = true,
  projectsError = null,
  currentProjectId,
  pick,
  lastUse,
  nav,
  hidden = false,
  onCreated,
  onAnswerLost,
  onOpenSettings,
  onOpenProjects,
}: {
  projects: readonly Project[];
  // The project list has answered; projectsError is its failure.
  projectsLoaded?: boolean;
  projectsError?: ErrorCode | null;
  // The project of the session on screen, the default project.
  currentProjectId?: string;
  // The project a project's "+" asked for; a new seq is a new ask.
  pick?: { projectId: string; seq: number };
  // When each project was last worked in (ms): a long project menu lists
  // the most recent first.
  lastUse?: ReadonlyMap<string, number>;
  nav?: ReactNode;
  hidden?: boolean;
  onCreated: (session: Session) => void;
  // A start whose answer was lost may have made the session: the app reloads
  // its session list, so the session shows before a retry is offered.
  onAnswerLost?: () => Promise<void>;
  onOpenSettings: () => void;
  onOpenProjects: () => void;
}) {
  const { t } = useTranslation("composer");
  const agentName = useAgentName();
  const [catalogState, retry] = useCatalog(true);
  const [prefs] = useState(() => loadPrefs());
  const [projectId, setProjectId] = useState<string | undefined>(
    pick?.projectId,
  );
  // A project's "+" in the sidebar (pick) switches the draft to that
  // project as a pick in the project menu does; not while a start runs.
  const [pickSeen, setPickSeen] = useState(pick?.seq);
  // Repositories left out, per project: a project switched to starts with
  // all of its repositories.
  const [left, setLeft] = useState<Record<string, string[]>>({});
  // The picked model, as choiceKey(provider, model id).
  const [model, setModel] = useState<string>();
  const [mode, setMode] = useState<PermissionMode>();
  const [effort, setEffort] = useState<Effort>();
  const [auth, setAuth] = useState<ClaudeAuth>();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  // checking: the answer was lost and the session list is being reloaded.
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  const [lost, setLost] = useState(false);
  const draftId = useRef<string | null>(null);
  const inputId = useId();
  if (pick && pick.seq !== pickSeen && !busy) {
    setPickSeen(pick.seq);
    setProjectId(pick.projectId);
    setError(null);
  }

  // Each time it shows again, the catalog is asked again: a credential may
  // have been added in Settings meanwhile (the mount asks by itself).
  const shown = useRef(!hidden);
  useEffect(() => {
    if (!hidden && !shown.current) refreshCatalog();
    shown.current = !hidden;
  }, [hidden]);

  const project =
    projects.find((p) => p.id === projectId) ??
    defaultProject(projects, prefs, currentProjectId);
  const catalog: AgentCatalog | undefined =
    catalogState.state === "ready" ? catalogState.catalog : undefined;
  const picked = model ? parseChoiceKey(model) : undefined;
  const choice = catalog
    ? (findModel(catalog, picked?.model, picked?.provider) ??
      defaultChoice(catalog, prefs))
    : undefined;
  const modes = (catalog?.modes ?? []).filter((m) => m in modeKeys);
  const chosenMode =
    mode ?? (catalog ? defaultMode({ ...catalog, modes }) : undefined);
  const offeredEfforts = efforts(choice?.provider);
  const chosenEffort =
    effort && offeredEfforts.includes(effort)
      ? effort
      : choice
        ? defaultEffort(choice.provider, prefs)
        : undefined;
  const auths = choice?.provider.claudeAuths ?? [];
  const chosenAuth =
    choice?.provider.provider === "claude"
      ? auth && auths.includes(auth)
        ? auth
        : defaultAuth(choice.provider)
      : undefined;
  // A project with one repository has nothing to choose: that one goes in,
  // whatever was left out while it had more.
  const leftOut =
    project && project.repos.length > 1 ? (left[project.id] ?? []) : [];
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
  const setRepos = (ids: string[]) => {
    if (!project) return;
    setLeft((l) => ({
      ...l,
      [project.id]: project.repos
        .map((r) => r.id)
        .filter((id) => !ids.includes(id)),
    }));
  };

  const ready =
    !!project &&
    !!choice &&
    !!chosenMode &&
    repoIds.length > 0 &&
    !busy &&
    !checking;

  const start = () => {
    const body = text.trim();
    if (!ready || !project || !choice || !chosenMode || body === "") return;
    setBusy(true);
    setError(null);
    setLost(false);
    // One client id per draft, kept across retries: the server makes one
    // session per message.clientId, so a retry after a lost answer does not
    // start a second one. A new text is a new draft.
    draftId.current ??= crypto.randomUUID();
    // The draft's project stays as started, whatever is opened meanwhile.
    setProjectId(project.id);
    createSession({
      projectId: project.id,
      repoIds,
      provider: choice.provider.provider,
      claudeAuth: chosenAuth,
      model: choice.model.id,
      effort: chosenEffort,
      mode: chosenMode,
      message: { clientId: draftId.current, text: body },
    }).then(
      (session) => {
        savePrefs({
          projectId: project.id,
          provider: choice.provider.provider,
          model: choice.model.id,
          effort: chosenEffort,
        });
        onCreated(session);
      },
      (err: unknown) => {
        setError(errorCode(err));
        setBusy(false);
        if (!answerLost(err) || !onAnswerLost) return;
        // The session may exist: show it in the list before a retry.
        setLost(true);
        setChecking(true);
        onAnswerLost()
          .catch(() => {})
          .finally(() => setChecking(false));
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

  // A long project menu searches and lists the most recently used first
  // (the list's order breaks ties); a short one keeps the list's order.
  const longMenu = projects.length >= PROJECT_SEARCH_FROM;
  const byUse = longMenu
    ? projects
        .map((p, i) => ({ p, i, at: lastUse?.get(p.id) ?? -Infinity }))
        .sort((a, b) => b.at - a.at || a.i - b.i)
        .map((x) => x.p)
    : projects;
  const projectOptions: PickerOption<string>[] = byUse.map((p) => ({
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
      value: choiceKey(p.provider, m.id),
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
  const effortOptions: PickerOption<Effort>[] = offeredEfforts.map((e) => ({
    value: e,
    label: t(effortKeys[e]),
    description: e === "default" ? t("effortDefaultHint") : undefined,
  }));
  const authOptions: PickerOption<ClaudeAuth>[] = auths
    .filter((a) => a in authKeys)
    .map((a) => ({ value: a, label: t(authKeys[a]) }));

  let dock: ReactNode;
  if (!project && !projectsLoaded) {
    dock = (
      <p className="start-notice" role="status">
        {t("projectsLoading")}
      </p>
    );
  } else if (!project && projectsError) {
    dock = (
      <div className="start-notice" role="alert">
        <p>
          {t("projectsFailed")} {t(projectsError, { ns: "errors" })}
        </p>
      </div>
    );
  } else if (!project) {
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
              setLost(false);
              // A draft keeps its project: opening a session of another
              // project meanwhile must not move it there.
              if (projectId === undefined) setProjectId(project.id);
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
              value={choiceKey(choice.provider.provider, choice.model.id)}
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
            {chosenEffort && (
              <Picker
                label={t("effortOf", { effort: t(effortKeys[chosenEffort]) })}
                heading={t("effort")}
                note={t("effortLowered")}
                value={chosenEffort}
                options={effortOptions}
                onChange={setEffort}
              >
                <Icon name="gauge" />
                <span>{t(effortKeys[chosenEffort])}</span>
              </Picker>
            )}
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
            {lost && <> {checking ? t("startLostChecking") : t("startLost")}</>}
          </p>
        )}
        <p className="comp-note">{t("runsIn", { project: project.name })}</p>
      </>
    );
  }

  return (
    <section className="view" aria-label={t("newTitle")} hidden={hidden}>
      <header className="hdr">
        <MenuButton />
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
                search={
                  longMenu
                    ? {
                        label: t("projectSearch"),
                        placeholder: t("projectSearchPlaceholder"),
                        empty: t("projectSearchNone"),
                      }
                    : undefined
                }
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
                <RepoChoice
                  repos={project.repos}
                  picked={repoIds}
                  disabled={busy}
                  onToggle={toggleRepo}
                  onChange={setRepos}
                />
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
