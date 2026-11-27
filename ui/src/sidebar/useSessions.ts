import { useCallback, useEffect, useRef, useState } from "react";

import {
  errorCode,
  listProjects,
  listSessions,
  streamAppEvents,
  type EventSourceCtor,
} from "../api";
import type {
  AppEvent,
  ErrorCode,
  Project,
  SessionStatus,
  SessionSummary,
} from "../api/types.gen";

// The fields of a session the owner changes with updateSession.
export type SessionPatch = Partial<
  Pick<SessionSummary, "title" | "model" | "mode">
>;

export interface Lists {
  sessions: SessionSummary[];
  projects: Project[];
}

function upsert<T extends { id: string }>(list: T[], item: T): T[] {
  const i = list.findIndex((x) => x.id === item.id);
  return i < 0 ? [...list, item] : list.map((x, j) => (j === i ? item : x));
}

// applyAppEvent changes the lists by one /api/events frame; kinds the
// sidebar does not show leave them as they are.
export function applyAppEvent(lists: Lists, e: AppEvent): Lists {
  switch (e.kind) {
    case "session.updated":
      return e.session
        ? { ...lists, sessions: upsert(lists.sessions, e.session) }
        : lists;
    case "session.removed":
      return {
        ...lists,
        sessions: lists.sessions.filter((s) => s.id !== e.sessionId),
      };
    case "project.updated":
      return e.project
        ? { ...lists, projects: upsert(lists.projects, e.project) }
        : lists;
    case "project.removed":
      return {
        ...lists,
        projects: lists.projects.filter((p) => p.id !== e.projectId),
      };
    case "sandbox.updated": {
      const sandbox = e.sandbox;
      return sandbox
        ? {
            ...lists,
            projects: lists.projects.map((p) =>
              p.id === e.projectId ? { ...p, sandbox } : p,
            ),
          }
        : lists;
    }
    default:
      return lists;
  }
}

// applyAnswer changes the lists by the answer to the owner's own request.
// An answer is no event: one that comes back after the stream has shown a
// newer state must not set it back. So a project or session already known is
// left as the stream keeps it (a creation's answer only adds it), and removals
// are applied as they are (they cannot be stale).
export function applyAnswer(lists: Lists, e: AppEvent): Lists {
  switch (e.kind) {
    case "project.updated":
      return lists.projects.some((p) => p.id === e.projectId)
        ? lists
        : applyAppEvent(lists, e);
    case "session.updated":
      return !e.session || lists.sessions.some((s) => s.id === e.session?.id)
        ? lists
        : applyAppEvent(lists, e);
    case "project.removed":
    case "session.removed":
      return applyAppEvent(lists, e);
    default:
      return lists;
  }
}

export interface SessionsState extends Lists {
  // The first load has answered (with lists or an error).
  loaded: boolean;
  // error is the session list's load failure, projectsError the project
  // list's: the sidebar works without projects, the Projects page does not.
  error: ErrorCode | null;
  projectsError: ErrorCode | null;
  // setStatus applies a session.status from the session's own stream.
  setStatus: (sessionId: string, status: SessionStatus) => void;
  // apply changes the lists by an answer of the owner's own request, without
  // waiting for the stream to echo it (applyAnswer).
  apply: (e: AppEvent) => void;
  setSeen: (sessionId: string) => void;
  patch: (sessionId: string, fields: SessionPatch) => void;
  // reload asks for both lists again; it settles when they are applied.
  reload: () => Promise<void>;
}

const empty: Lists = { sessions: [], projects: [] };

// A change made while a load is under way, applied again on top of its
// answer: a stream event, or an answer of the owner's own request.
type Change = { e: AppEvent; answer: boolean };

const replayChange = (lists: Lists, c: Change) =>
  c.answer ? applyAnswer(lists, c.e) : applyAppEvent(lists, c.e);

// useSessions keeps the session and project lists: loaded on mount and on
// every (re)open of /api/events (it has no replay), changed by its events in
// between. Events that arrive while a load is under way are applied again on
// top of its answer; only the newest of overlapping loads counts.
export function useSessions(EventSource?: EventSourceCtor): SessionsState {
  const [lists, setLists] = useState<Lists>(empty);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  const [projectsError, setProjectsError] = useState<ErrorCode | null>(null);
  const generation = useRef(0);
  // Changes since the newest load started; null while no load is under way.
  const during = useRef<Change[] | null>(null);

  const reload = useCallback((): Promise<void> => {
    const gen = ++generation.current;
    during.current = [];
    // The sidebar works without project names; the failure is kept for the
    // Projects page, which must not show "no projects" for it.
    let projectsFailed: ErrorCode | null = null;
    const projects = listProjects().then(
      (list) => list.items,
      (err: unknown) => {
        projectsFailed = errorCode(err);
        return null;
      },
    );
    return Promise.all([listSessions(), projects]).then(
      ([sessions, projectItems]) => {
        if (gen !== generation.current) return;
        const replay = during.current ?? [];
        during.current = null;
        setLists((prev) =>
          replay.reduce(replayChange, {
            sessions: sessions.items,
            projects: projectItems ?? prev.projects,
          }),
        );
        setError(null);
        setProjectsError(projectsFailed);
        setLoaded(true);
      },
      (err: unknown) => {
        if (gen !== generation.current) return;
        during.current = null;
        setError(errorCode(err));
        setLoaded(true);
      },
    );
  }, []);

  useEffect(() => {
    void reload();
    return streamAppEvents({
      EventSource,
      onOpen: () => void reload(),
      onEvent: (e) => {
        during.current?.push({ e, answer: false });
        setLists((prev) => applyAppEvent(prev, e));
      },
    });
  }, [EventSource, reload]);

  const setStatus = useCallback(
    (sessionId: string, status: SessionStatus) =>
      setLists((prev) => ({
        ...prev,
        sessions: prev.sessions.map((s) =>
          s.id === sessionId && s.status !== status ? { ...s, status } : s,
        ),
      })),
    [],
  );

  const apply = useCallback((e: AppEvent) => {
    during.current?.push({ e, answer: true });
    setLists((prev) => applyAnswer(prev, e));
  }, []);

  // setSeen clears a session's unseen mark once the owner has seen it, before
  // the stream's session.updated says so.
  const setSeen = useCallback(
    (sessionId: string) =>
      setLists((prev) =>
        prev.sessions.some((s) => s.id === sessionId && s.unseen)
          ? {
              ...prev,
              sessions: prev.sessions.map((s) =>
                s.id === sessionId ? { ...s, unseen: false } : s,
              ),
            }
          : prev,
      ),
    [],
  );

  // patch applies the fields the owner just changed (the answer of an update):
  // only those, so an older answer cannot set back a newer status.
  const patch = useCallback(
    (sessionId: string, fields: SessionPatch) =>
      setLists((prev) => ({
        ...prev,
        sessions: prev.sessions.map((s) =>
          s.id === sessionId ? { ...s, ...fields } : s,
        ),
      })),
    [],
  );

  return {
    ...lists,
    loaded,
    error,
    projectsError,
    setStatus,
    setSeen,
    patch,
    apply,
    reload,
  };
}
