import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { EventSourceCtor } from "./api";
import type { SessionStatus } from "./api/types.gen";
import { SessionView } from "./shell";
import { orderSessions, Sidebar, useSessions } from "./sidebar";

// The app: the session sidebar and the selected session. EventSource is
// injectable for tests (jsdom has none).
export function App({ EventSource }: { EventSource?: EventSourceCtor } = {}) {
  const { t } = useTranslation("shell");
  const { sessions, projects, loaded, error, setStatus } =
    useSessions(EventSource);
  const [picked, setPicked] = useState<string | null>(null);

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

  const project = projects.find((p) => p.id === selected?.projectId);
  return (
    <div className="app">
      <Sidebar
        sessions={sessions}
        projects={projects}
        selectedId={selectedId}
        onSelect={setPicked}
        loaded={loaded}
        error={error}
      />
      <main className="main">
        {selected ? (
          <SessionView
            key={selected.id}
            session={selected}
            projectName={project?.name}
            EventSource={EventSource}
            onStatus={onStatus}
          />
        ) : (
          loaded &&
          !error && (
            <div className="empty-start">
              <p className="big">{t("empty")}</p>
              <p>{t("emptyHint")}</p>
            </div>
          )
        )}
      </main>
    </div>
  );
}
