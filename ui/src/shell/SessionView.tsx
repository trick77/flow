import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { EventSourceCtor } from "../api";
import type { SessionStatus, SessionSummary } from "../api/types.gen";
import { Composer } from "../composer";
import {
  TerminalDrawer,
  TerminalToggle,
  type EmulatorFactory,
  type WebSocketCtor,
} from "../terminal";
import { foldEvents, Timeline, useSessionStream } from "../timeline";
import { Header } from "./Header";

// SessionView is one session: header, timeline, composer, terminal drawer.
// Mount it with key={session.id}: a switch starts a fresh stream.
export function SessionView({
  session,
  projectName,
  EventSource,
  WebSocket,
  createEmulator,
  onStatus,
}: {
  session: SessionSummary;
  projectName?: string;
  EventSource?: EventSourceCtor;
  WebSocket?: WebSocketCtor;
  createEmulator?: EmulatorFactory;
  onStatus?: (status: SessionStatus) => void;
}) {
  const { t } = useTranslation("shell");
  const stream = useSessionStream(session.id, { EventSource, onStatus });
  const folded = useMemo(() => foldEvents(stream), [stream]);
  const status = folded.status ?? session.status;
  const [terminalOpen, setTerminalOpen] = useState(false);
  const tab = useMemo(
    () => ({
      key: `${session.id}-1`,
      projectId: session.projectId,
      sessionId: session.id,
    }),
    [session.id, session.projectId],
  );
  return (
    <section className="view" aria-label={t("session")}>
      <Header
        project={projectName}
        title={session.title}
        branch={session.branch}
        actions={
          <TerminalToggle
            pressed={terminalOpen}
            onToggle={() => setTerminalOpen((o) => !o)}
          />
        }
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
        <TerminalDrawer
          tab={tab}
          open={terminalOpen}
          onClose={() => setTerminalOpen(false)}
          WebSocket={WebSocket}
          createEmulator={createEmulator}
        />
      </div>
    </section>
  );
}
