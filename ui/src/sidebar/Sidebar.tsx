import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import type {
  ErrorCode,
  Project,
  SessionSummary,
  SidebarGrouping,
} from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { AgentPill } from "../shell/AgentPill";
import { projectBadge } from "../shell/badge";
import { SIDEBAR_ID } from "../shell/drawer";
import { sandboxKeys } from "../shell/Header";
import { Icon } from "../shell/Icon";
import { UserMenu } from "../shell/UserMenu";
import { sandboxDot } from "./sandbox";
import {
  groupByProject,
  groupSessions,
  orderSessions,
  SECTION_ORDER,
  type SectionId,
} from "./group";
import { RelativeTime } from "./RelativeTime";
import { SearchPalette } from "./SearchPalette";
import { isSearchShortcut, shortcutKey } from "./shortcut";
import { showsTime, statusClass, statusKey } from "./status";
import "./sidebar.css";

const sectionKeys = {
  needs: "sectionNeeds",
  working: "sectionWorking",
  done: "sectionDone",
  idle: "sectionIdle",
} as const satisfies Record<SectionId, string>;

// RowStatus is the row's top right: the status word, or for an idle or a
// seen done row how long ago its last activity was (showsTime).
function RowStatus({ session }: { session: SessionSummary }) {
  const { t } = useTranslation("sidebar");
  if (showsTime(session)) {
    return <RelativeTime className="s-time" at={session.lastActivityAt} />;
  }
  return (
    <span className={`status ${statusClass(session.status)}`}>
      {t(statusKey(session.status))}
    </span>
  );
}

function SessionRow({
  session,
  project,
  selected,
  onSelect,
}: {
  session: SessionSummary;
  project?: Project;
  selected: boolean;
  onSelect: (id: string) => void;
}) {
  const { t } = useTranslation("sidebar");
  const name = project?.name ?? "";
  return (
    <button
      className={session.unseen ? "srow unseen" : "srow"}
      type="button"
      aria-current={selected ? "true" : undefined}
      aria-description={session.unseen ? t("unseen") : undefined}
      onClick={() => onSelect(session.id)}
    >
      <span className="s-proj">
        <span className="mono-badge" aria-hidden="true">
          {projectBadge(name)}
        </span>
        <span>{name}</span>
      </span>
      <span className="s-right">
        <RowStatus session={session} />
      </span>
      <span className="s-title">{session.title}</span>
      <span className="s-branch">
        <Icon name="branch" />
        <span>{session.branch}</span>
        {session.repos.length > 1 && (
          <span className="s-repos">
            {t("repoCount", {
              count: session.repos.length,
              n: formatNumber(session.repos.length),
            })}
          </span>
        )}
      </span>
      <span className="s-right">
        <AgentPill provider={session.provider} />
      </span>
    </button>
  );
}

// SandboxDot is a project's sandbox state as a dot, named for screen readers
// and in its tooltip.
function SandboxDotMark({ project }: { project: Project }) {
  const { t } = useTranslation("shell");
  const label = t(sandboxKeys[project.sandbox.state] ?? "sandboxChipNone");
  return (
    <span
      className={`sb-dot dot-${sandboxDot(project.sandbox)}`}
      role="img"
      aria-label={label}
      title={label}
    />
  );
}

// AddButton is the "+" that starts a new session in one project.
function AddButton({
  project,
  onAdd,
}: {
  project: Project;
  onAdd: (projectId: string) => void;
}) {
  const { t } = useTranslation("sidebar");
  const label = t("newSessionIn", { project: project.name });
  return (
    <button
      type="button"
      className="icon-btn sb-add"
      aria-label={label}
      title={label}
      onClick={() => onAdd(project.id)}
    >
      <Icon name="plus" />
    </button>
  );
}

// ProjectHead is a project with sessions in the "By project" grouping: its
// badge, name and session count, the sandbox dot and the "+".
function ProjectHead({
  project,
  count,
  onAdd,
}: {
  project: Project;
  count: number;
  onAdd?: (projectId: string) => void;
}) {
  return (
    <h2 className="sb-section-h sb-proj-h">
      <span className="mono-badge" aria-hidden="true">
        {projectBadge(project.name)}
      </span>
      <span className="nm">{project.name}</span>
      <span className="n">{formatNumber(count)}</span>
      <span className="grow" />
      <SandboxDotMark project={project} />
      {onAdd && <AddButton project={project} onAdd={onAdd} />}
    </h2>
  );
}

// IdleProjects is the projects without sessions, one line each after the
// sections: a project a day old and forty never used stay a short, calm
// list under the work that runs. A click on a line starts a session in it,
// as its "+" does (the "+" is the keyboard's way).
function IdleProjects({
  projects,
  onAdd,
}: {
  projects: Project[];
  onAdd?: (projectId: string) => void;
}) {
  const { t } = useTranslation("sidebar");
  return (
    <section className="sb-section sb-idle" aria-label={t("noSessionsYet")}>
      <h2 className="sb-section-h">
        <span>{t("noSessionsYet")}</span>
        <span className="n">{formatNumber(projects.length)}</span>
      </h2>
      <ul className="sb-plist">
        {projects.map((p) => (
          <li
            key={p.id}
            className="sb-prow"
            onClick={(e) => {
              if (!(e.target as HTMLElement).closest("button")) onAdd?.(p.id);
            }}
          >
            <span className="mono-badge" aria-hidden="true">
              {projectBadge(p.name)}
            </span>
            <span className="nm">{p.name}</span>
            <SandboxDotMark project={p} />
            {onAdd && <AddButton project={p} onAdd={onAdd} />}
          </li>
        ))}
      </ul>
    </section>
  );
}

// GroupingSwitch is the "By status" / "By project" radio group; Left, Right,
// Up and Down move between the two as in any radio group.
function GroupingSwitch({
  grouping,
  onChange,
}: {
  grouping: SidebarGrouping;
  onChange: (g: SidebarGrouping) => void;
}) {
  const { t } = useTranslation("sidebar");
  const options: [SidebarGrouping, string][] = [
    ["status", t("groupByStatus")],
    ["project", t("groupByProject")],
  ];
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key))
      return;
    e.preventDefault();
    const next = grouping === "status" ? "project" : "status";
    onChange(next);
    e.currentTarget
      .querySelector<HTMLElement>(`[data-grouping="${next}"]`)
      ?.focus();
  };
  return (
    <div
      className="seg"
      role="radiogroup"
      aria-label={t("groupLabel")}
      onKeyDown={onKeyDown}
    >
      {options.map(([g, label]) => (
        <button
          key={g}
          type="button"
          role="radio"
          data-grouping={g}
          aria-checked={grouping === g}
          tabIndex={grouping === g ? 0 : -1}
          onClick={() => onChange(g)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

// RailList is the collapsed sidebar's sessions: the project badge with a dot
// for any row that shows its status word (not showsTime), in the status order.
function RailList({
  sessions,
  projects,
  selectedId,
  onSelect,
}: {
  sessions: SessionSummary[];
  projects: Map<string, Project>;
  selectedId?: string;
  onSelect: (id: string) => void;
}) {
  const { t } = useTranslation("sidebar");
  return (
    <nav className="rail-col rail-list" aria-label={t("railLabel")}>
      {orderSessions(sessions).map((s) => {
        const name = projects.get(s.projectId)?.name ?? "";
        const idle = showsTime(s);
        return (
          <button
            key={s.id}
            type="button"
            className="rail-sess"
            aria-current={s.id === selectedId ? "true" : undefined}
            aria-label={
              idle
                ? t("railSession", { project: name, title: s.title })
                : t("railSessionStatus", {
                    project: name,
                    title: s.title,
                    status: t(statusKey(s.status)),
                  })
            }
            title={s.title}
            onClick={() => onSelect(s.id)}
          >
            <span className="mono-badge" aria-hidden="true">
              {projectBadge(name)}
            </span>
            {!idle && (
              <span
                className={`rail-dot ${statusClass(s.status)}`}
                aria-hidden="true"
              />
            )}
          </button>
        );
      })}
    </nav>
  );
}

const FOCUSABLE =
  'button:not([disabled]), [href], input:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Sidebar: the wordmark with the search and collapse buttons, New session,
// the grouping and the sessions in their sections; the account at the foot.
// Collapsed (rail) it is a column of icons. On narrow screens it is a drawer
// (drawerOpen), a modal dialog while open. Cmd+K or Ctrl+K opens the search
// from anywhere.
export function Sidebar({
  sessions,
  projects,
  selectedId,
  onSelect,
  onNewSession,
  newSessionOpen = false,
  loaded,
  error,
  grouping = "status",
  onGrouping,
  rail = false,
  onRail,
  narrow = false,
  drawerOpen = false,
  onCloseDrawer,
  onOpenSettings,
  onOpenProjects,
  projectsError = null,
}: {
  sessions: SessionSummary[];
  projects: Project[];
  selectedId?: string;
  onSelect: (id: string) => void;
  // projectId: the project to start in (a project's "+"); without one the
  // new session's own default.
  onNewSession?: (projectId?: string) => void;
  onOpenProjects?: () => void;
  // The project list failed to load: an empty list is not "no projects".
  projectsError?: ErrorCode | null;
  // newSessionOpen marks New session as the view shown.
  newSessionOpen?: boolean;
  loaded: boolean;
  error: ErrorCode | null;
  grouping?: SidebarGrouping;
  onGrouping?: (g: SidebarGrouping) => void;
  // rail: collapsed to the icon column (wide screens only).
  rail?: boolean;
  onRail?: (rail: boolean) => void;
  narrow?: boolean;
  drawerOpen?: boolean;
  onCloseDrawer?: () => void;
  onOpenSettings?: () => void;
}) {
  const { t } = useTranslation("sidebar");
  const [searching, setSearching] = useState(false);
  const projectOf = new Map(projects.map((p) => [p.id, p]));
  const compact = rail && !narrow;
  const modal = narrow && drawerOpen;
  const aside = useRef<HTMLElement>(null);

  // Capture phase on window: the terminal and the composer must not swallow
  // the shortcut before it gets here.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isSearchShortcut(e)) return;
      e.preventDefault();
      e.stopPropagation();
      // A held key repeats keydown: only the first press toggles.
      if (e.repeat) return;
      setSearching((s) => !s);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);

  // The drawer takes focus when it opens.
  useEffect(() => {
    if (!modal) return;
    aside.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
  }, [modal]);

  // As a dialog: Escape closes it, Tab stays inside. Keys from the search
  // palette (a portal, outside this element) are not the drawer's.
  const onKeyDown = (e: ReactKeyboardEvent<HTMLElement>) => {
    if (!modal || !aside.current?.contains(e.target as Node)) return;
    if (e.key === "Escape") {
      e.preventDefault();
      onCloseDrawer?.();
      return;
    }
    if (e.key !== "Tab") return;
    const items = [...aside.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  const select = (id: string) => {
    onSelect(id);
    if (narrow) onCloseDrawer?.();
  };
  const startNew = onNewSession
    ? (projectId?: string) => {
        onNewSession(projectId);
        if (narrow) onCloseDrawer?.();
      }
    : undefined;
  const openProjects = onOpenProjects
    ? () => {
        onOpenProjects();
        if (narrow) onCloseDrawer?.();
      }
    : undefined;
  const settings = onOpenSettings
    ? () => {
        onOpenSettings();
        if (narrow) onCloseDrawer?.();
      }
    : undefined;

  const collapse = (
    <button
      type="button"
      className="icon-btn collapse-btn"
      aria-label={
        narrow ? t("closeSidebar") : compact ? t("expand") : t("collapse")
      }
      aria-pressed={narrow ? undefined : compact}
      aria-controls={narrow ? undefined : SIDEBAR_ID}
      title={narrow ? t("closeSidebar") : compact ? t("expand") : t("collapse")}
      onClick={() => (narrow ? onCloseDrawer?.() : onRail?.(!compact))}
    >
      <Icon name="sidebar" />
    </button>
  );

  const rows = (list: SessionSummary[]) =>
    list.map((s) => (
      <SessionRow
        key={s.id}
        session={s}
        project={projectOf.get(s.projectId)}
        selected={s.id === selectedId}
        onSelect={select}
      />
    ));

  // Without any project there is nothing to start: say so and point to the
  // projects page (unless the list failed to load; the error shows then).
  const noProjects =
    projects.length === 0 && !projectsError ? (
      <div className="sb-none">
        <p className="sb-empty">{t("noProjects")}</p>
        {openProjects && (
          <button type="button" className="sb-link" onClick={openProjects}>
            {t("addProject")}
          </button>
        )}
      </div>
    ) : null;

  const sectionList = () => {
    if (grouping === "project") {
      const groups = groupByProject(sessions, projects);
      if (groups.length === 0)
        return noProjects ?? <p className="sb-empty">{t("empty")}</p>;
      const idle = groups.flatMap((g) =>
        g.project && g.sessions.length === 0 ? [g.project] : [],
      );
      return (
        <>
          {groups.map((g) => {
            if (g.sessions.length === 0) return null;
            if (!g.project) {
              const label = t("otherProjects");
              return (
                <section className="sb-section" key="" aria-label={label}>
                  <h2 className="sb-section-h">
                    <span>{label}</span>
                    <span className="n">{formatNumber(g.sessions.length)}</span>
                  </h2>
                  {rows(g.sessions)}
                </section>
              );
            }
            return (
              <section
                className="sb-section"
                key={g.project.id}
                aria-label={g.project.name}
              >
                <ProjectHead
                  project={g.project}
                  count={g.sessions.length}
                  onAdd={startNew}
                />
                {rows(g.sessions)}
              </section>
            );
          })}
          {idle.length > 0 && <IdleProjects projects={idle} onAdd={startNew} />}
        </>
      );
    }
    // By status stays as it was without sessions: New session sits right
    // above, and the centre says "No sessions yet" with its own button.
    const sections = groupSessions(sessions);
    return SECTION_ORDER.map((id) => (
      <section className="sb-section" key={id} aria-label={t(sectionKeys[id])}>
        <h2 className="sb-section-h">
          <span>{t(sectionKeys[id])}</span>
          <span className="n">{formatNumber(sections[id].length)}</span>
        </h2>
        {sections[id].length === 0 ? (
          <p className="sb-empty">{t("empty")}</p>
        ) : (
          rows(sections[id])
        )}
      </section>
    ));
  };

  const searchTitle = t("searchTitle", { shortcut: t(shortcutKey()) });
  return (
    <aside
      ref={aside}
      id={SIDEBAR_ID}
      className="sidebar"
      aria-label={t("label")}
      role={modal ? "dialog" : undefined}
      aria-modal={modal ? true : undefined}
      inert={narrow && !drawerOpen}
      onKeyDown={onKeyDown}
    >
      <div className="sb-top">
        <h1 className={compact ? "sr" : "wordmark"}>
          {t("appName", { ns: "common" })}
        </h1>
        {compact ? (
          collapse
        ) : (
          <span className="sb-top-btns">
            <button
              type="button"
              className="icon-btn"
              aria-label={t("search")}
              aria-haspopup="dialog"
              aria-keyshortcuts="Meta+K Control+K"
              title={searchTitle}
              onClick={() => setSearching(true)}
            >
              <Icon name="search" />
            </button>
            {collapse}
          </span>
        )}
      </div>
      {compact ? (
        <>
          <div className="rail-col">
            {startNew && (
              <button
                type="button"
                className="icon-btn"
                aria-label={t("newSession")}
                aria-current={newSessionOpen ? "page" : undefined}
                title={t("newSession")}
                onClick={() => startNew()}
              >
                <Icon name="plus" />
              </button>
            )}
            <button
              type="button"
              className="icon-btn"
              aria-label={t("search")}
              aria-haspopup="dialog"
              aria-keyshortcuts="Meta+K Control+K"
              title={searchTitle}
              onClick={() => setSearching(true)}
            >
              <Icon name="search" />
            </button>
          </div>
          {loaded && (
            <RailList
              sessions={sessions}
              projects={projectOf}
              selectedId={selectedId}
              onSelect={select}
            />
          )}
          <span className="rail-grow" />
          <UserMenu compact onOpenSettings={settings} />
        </>
      ) : (
        <>
          <div className="sb-tools">
            {startNew && (
              <button
                type="button"
                className="nav-row"
                aria-current={newSessionOpen ? "page" : undefined}
                onClick={() => startNew()}
              >
                <span className="plus-disc" aria-hidden="true">
                  <Icon name="plus" />
                </span>
                <span>{t("newSession")}</span>
              </button>
            )}
            {onGrouping && (
              <GroupingSwitch grouping={grouping} onChange={onGrouping} />
            )}
          </div>
          <nav className="sb-list" aria-label={t("listLabel")}>
            {error && (
              <p className="sb-error" role="alert">
                {t(error, { ns: "errors" })}
              </p>
            )}
            {!loaded && <p className="sb-empty">{t("loading")}</p>}
            {loaded && sectionList()}
          </nav>
          <UserMenu onOpenSettings={settings} />
        </>
      )}
      {searching &&
        createPortal(
          <SearchPalette
            sessions={sessions}
            projects={projects}
            onSelect={select}
            onNewSession={startNew}
            onClose={() => setSearching(false)}
          />,
          document.body,
        )}
    </aside>
  );
}
