import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { EventSourceCtor } from "./api";
import type { Session, SessionStatus } from "./api/types.gen";
import { NewSession, useKnownCatalog } from "./composer";
import { ProjectsPage, SettingsPage } from "./pages";
import { Nav, SessionView, type View } from "./shell";
import {
  orderSessions,
  Sidebar,
  useSessions,
  type SessionPatch,
} from "./sidebar";

// The app: the session sidebar and, in the centre, the selected session, the
// start of a new one, or one of the pages the header's Projects and Settings
// buttons open. EventSource is injectable for tests (jsdom has none).
export function App({ EventSource }: { EventSource?: EventSourceCtor } = {}) {
  const { t } = useTranslation("shell");
  const {
    sessions,
    projects,
    loaded,
    error,
    projectsError,
    setStatus,
    setSeen,
    patch,
    apply,
    reload,
  } = useSessions(EventSource);
  const [picked, setPicked] = useState<string | null>(null);
  const [view, setView] = useState<View>("session");
  // New session mounts on first open and then stays mounted, hidden under
  // the other views: its draft and a start under way survive a look
  // elsewhere. A new key after a start begins the next one empty.
  const [newOpened, setNewOpened] = useState(false);
  const [newKey, setNewKey] = useState(0);
  const viewRef = useRef(view);
  useEffect(() => {
    viewRef.current = view;
  });
  // The catalog names the models in the composers; one shared state, so a
  // load that succeeds anywhere (New session) names them here too.
  const catalog = useKnownCatalog();

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
  const onSeen = useCallback(() => {
    if (selectedId) setSeen(selectedId);
  }, [selectedId, setSeen]);
  const onUpdated = useCallback(
    (fields: SessionPatch) => {
      if (selectedId) patch(selectedId, fields);
    },
    [selectedId, patch],
  );
  // Picking a session in the sidebar shows it, from any page.
  const onSelect = useCallback((id: string) => {
    setPicked(id);
    setView("session");
  }, []);
  // A session just started is listed at once, before the stream announces
  // it, and opens if New session is still on screen; started from a hidden
  // New session it stays in the list and the view stays as it is.
  const onCreated = useCallback(
    (s: Session) => {
      apply({
        kind: "session.updated",
        session: s.summary,
        sessionId: s.summary.id,
      });
      if (viewRef.current === "new") onSelect(s.summary.id);
      setNewKey((k) => k + 1);
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
  const startNew = () => {
    setNewOpened(true);
    setView("new");
  };
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
        {newOpened && (
          <NewSession
            key={newKey}
            hidden={view !== "new"}
            projects={projects}
            projectsLoaded={loaded}
            projectsError={projectsError ?? error}
            currentProjectId={selected?.projectId}
            nav={nav}
            onCreated={onCreated}
            onAnswerLost={reload}
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
            onSeen={onSeen}
            onUpdated={onUpdated}
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
