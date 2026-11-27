import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { EventSourceCtor } from "./api";
import type { Session, SessionStatus } from "./api/types.gen";
import { NewSession, useCatalog } from "./composer";
import { ProjectsPage, SettingsPage } from "./pages";
import { Nav, SessionView, type View } from "./shell";
import { orderSessions, Sidebar, useSessions } from "./sidebar";

// The app: the session sidebar and, in the centre, the selected session, the
// start of a new one, or one of the pages the header's Projects and Settings
// buttons open. EventSource is injectable for tests (jsdom has none).
export function App({ EventSource }: { EventSource?: EventSourceCtor } = {}) {
  const { t } = useTranslation("shell");
  const { sessions, projects, loaded, error, projectsError, setStatus, apply } =
    useSessions(EventSource);
  const [picked, setPicked] = useState<string | null>(null);
  const [view, setView] = useState<View>("session");
  // The catalog names the models in the composers; a failure only leaves
  // the model ids showing.
  const [catalogState] = useCatalog();
  const catalog =
    catalogState.state === "ready" ? catalogState.catalog : undefined;

  // The picked session while it exists, else the first row in the sidebar.
  const ordered = useMemo(() => orderSessions(sessions), [sessions]);
  const selected =
    sessions.find((s) => s.id === picked) ??
    (ordered.length > 0 ? ordered[0] : undefined);
  // Pin the automatic choice so a status change does not switch the view.
  if (selected && picked !== selected.id) setPicked(selected.id);

  const selectedId = selected?.id;
  const onStatus = useCallback(
    (status: SessionStatus) => {
      if (selectedId) setStatus(selectedId, status);
    },
    [selectedId, setStatus],
  );
  // Picking a session in the sidebar shows it, from any page.
  const onSelect = useCallback((id: string) => {
    setPicked(id);
    setView("session");
  }, []);
  // A session just started opens at once, before the stream announces it.
  const onCreated = useCallback(
    (s: Session) => {
      apply({
        kind: "session.updated",
        session: s.summary,
        sessionId: s.summary.id,
      });
      onSelect(s.summary.id);
    },
    [apply, onSelect],
  );

  const onArchived = useCallback(() => {
    if (selectedId) apply({ kind: "session.removed", sessionId: selectedId });
  }, [selectedId, apply]);

  const nav = <Nav view={view} onNavigate={setView} />;
  const back = selected ? () => setView("session") : undefined;
  const project = projects.find((p) => p.id === selected?.projectId);
  // Open sessions per project, for the Projects page's refusal of a delete
  // or rebuild.
  const openSessions = useMemo(() => {
    const n: Record<string, number> = {};
    for (const s of sessions) n[s.projectId] = (n[s.projectId] ?? 0) + 1;
    return n;
  }, [sessions]);
  const startNew = () => setView("new");
  return (
    <div className="app">
      <Sidebar
        sessions={sessions}
        projects={projects}
        selectedId={view === "session" ? selectedId : undefined}
        onSelect={onSelect}
        onNewSession={startNew}
        newSessionOpen={view === "new"}
        loaded={loaded}
        error={error}
      />
      <main className="main">
        {view === "new" && (
          <NewSession
            projects={projects}
            currentProjectId={selected?.projectId}
            nav={nav}
            onCreated={onCreated}
            onOpenSettings={() => setView("settings")}
            onOpenProjects={() => setView("projects")}
          />
        )}
        {view === "projects" && (
          <ProjectsPage
            projects={projects}
            openSessions={openSessions}
            loaded={loaded}
            error={projectsError ?? error}
            onEvent={apply}
            nav={nav}
            onBack={back}
          />
        )}
        {view === "settings" && <SettingsPage nav={nav} onBack={back} />}
        {/* The session stays mounted under a page: its draft, terminal and
            stream survive a look at Projects or Settings. */}
        {selected ? (
          <SessionView
            key={selected.id}
            session={selected}
            sandbox={project?.sandbox}
            projectName={project?.name}
            catalog={catalog}
            EventSource={EventSource}
            onStatus={onStatus}
            onArchived={onArchived}
            nav={nav}
            hidden={view !== "session"}
          />
        ) : view !== "session" ? null : (
          <section className="view" aria-label={t("session")}>
            <header className="hdr">
              <span className="hdr-spacer" />
              {nav}
            </header>
            {loaded && !error && (
              <div className="empty-start">
                <p className="big">{t("empty")}</p>
                <p>{t("emptyHint")}</p>
                <button
                  type="button"
                  className="btn primary empty-new"
                  onClick={startNew}
                >
                  {t("newSession")}
                </button>
              </div>
            )}
          </section>
        )}
      </main>
    </div>
  );
}
