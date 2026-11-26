import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import type { EventSourceCtor } from "../api";
import type { SessionStatus, SessionSummary } from "../api/types.gen";
import { Composer } from "../composer";
import { foldEvents, Timeline, useSessionStream } from "../timeline";
import { Header } from "./Header";

// SessionView is one session: header, timeline, composer. Mount it with
// key={session.id}: a switch starts a fresh stream.
export function SessionView({
  session,
  projectName,
  EventSource,
  onStatus,
}: {
  session: SessionSummary;
  projectName?: string;
  EventSource?: EventSourceCtor;
  onStatus?: (status: SessionStatus) => void;
}) {
  const { t } = useTranslation("shell");
  const stream = useSessionStream(session.id, { EventSource, onStatus });
  const folded = useMemo(() => foldEvents(stream), [stream]);
  const status = folded.status ?? session.status;
  return (
    <section className="view" aria-label={t("session")}>
      <Header
        project={projectName}
        title={session.title}
        branch={session.branch}
      />
      <div className="center-body">
        <Timeline
          sessionId={session.id}
          folded={folded}
          working={status === "working"}
          provider={session.provider}
        />
        <Composer
          sessionId={session.id}
          running={folded.turnOpen || status === "working"}
        />
      </div>
    </section>
  );
}
