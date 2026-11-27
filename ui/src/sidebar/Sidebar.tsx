import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import type { ErrorCode, Project, SessionSummary } from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { AgentPill } from "../shell/AgentPill";
import { projectBadge } from "../shell/badge";
import { Icon } from "../shell/Icon";
import { UserMenu } from "../shell/UserMenu";
import { groupSessions, SECTION_ORDER, type SectionId } from "./group";
import { SearchPalette } from "./SearchPalette";
import { isSearchShortcut, shortcutKey } from "./shortcut";
import { statusClass, statusKey } from "./status";
import "./sidebar.css";

const sectionKeys = {
  needs: "sectionNeeds",
  working: "sectionWorking",
  done: "sectionDone",
  idle: "sectionIdle",
} as const satisfies Record<SectionId, string>;

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
        <span className={`status ${statusClass(session.status)}`}>
          {t(statusKey(session.status))}
        </span>
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

// Sidebar: the wordmark with the search, New session, and the sessions in
// their status sections. Cmd+K or Ctrl+K opens the search from anywhere.
export function Sidebar({
  sessions,
  projects,
  selectedId,
  onSelect,
  onNewSession,
  newSessionOpen = false,
  loaded,
  error,
}: {
  sessions: SessionSummary[];
  projects: Project[];
  selectedId?: string;
  onSelect: (id: string) => void;
  onNewSession?: () => void;
  // newSessionOpen marks New session as the view shown.
  newSessionOpen?: boolean;
  loaded: boolean;
  error: ErrorCode | null;
}) {
  const { t } = useTranslation("sidebar");
  const [searching, setSearching] = useState(false);
  const sections = groupSessions(sessions);
  const projectOf = new Map(projects.map((p) => [p.id, p]));

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

  return (
    <aside className="sidebar" aria-label={t("label")}>
      <div className="sb-top">
        <h1 className="wordmark">{t("appName", { ns: "common" })}</h1>
        <button
          type="button"
          className="icon-btn"
          aria-label={t("search")}
          aria-haspopup="dialog"
          aria-keyshortcuts="Meta+K Control+K"
          title={t("searchTitle", { shortcut: t(shortcutKey()) })}
          onClick={() => setSearching(true)}
        >
          <Icon name="search" />
        </button>
      </div>
      {onNewSession && (
        <div className="sb-tools">
          <button
            type="button"
            className="nav-row"
            aria-current={newSessionOpen ? "page" : undefined}
            onClick={onNewSession}
          >
            <span className="plus-disc" aria-hidden="true">
              <Icon name="plus" />
            </span>
            <span>{t("newSession")}</span>
          </button>
        </div>
      )}
      <nav className="sb-list" aria-label={t("listLabel")}>
        {error && (
          <p className="sb-error" role="alert">
            {t(error, { ns: "errors" })}
          </p>
        )}
        {!loaded && <p className="sb-empty">{t("loading")}</p>}
        {loaded &&
          SECTION_ORDER.map((id) => (
            <section
              className="sb-section"
              key={id}
              aria-label={t(sectionKeys[id])}
            >
              <h2 className="sb-section-h">
                <span>{t(sectionKeys[id])}</span>
                <span className="n">{formatNumber(sections[id].length)}</span>
              </h2>
              {sections[id].length === 0 ? (
                <p className="sb-empty">{t("empty")}</p>
              ) : (
                sections[id].map((s) => (
                  <SessionRow
                    key={s.id}
                    session={s}
                    project={projectOf.get(s.projectId)}
                    selected={s.id === selectedId}
                    onSelect={onSelect}
                  />
                ))
              )}
            </section>
          ))}
      </nav>
      <UserMenu />
      {searching &&
        createPortal(
          <SearchPalette
            sessions={sessions}
            projects={projects}
            onSelect={onSelect}
            onClose={() => setSearching(false)}
          />,
          document.body,
        )}
    </aside>
  );
}
