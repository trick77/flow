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
    default:
      return lists;
  }
}

export interface SessionsState extends Lists {
  // The first load has answered (with lists or an error).
  loaded: boolean;
  error: ErrorCode | null;
  // setStatus applies a session.status from the session's own stream.
  setStatus: (sessionId: string, status: SessionStatus) => void;
}

const empty: Lists = { sessions: [], projects: [] };

// useSessions keeps the session and project lists: loaded on mount and on
// every (re)open of /api/events (it has no replay), changed by its events in
// between. Events that arrive while a load is under way are applied again on
// top of its answer; only the newest of overlapping loads counts.
export function useSessions(EventSource?: EventSourceCtor): SessionsState {
  const [lists, setLists] = useState<Lists>(empty);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  const generation = useRef(0);
  // Events since the newest load started; null while no load is under way.
  const during = useRef<AppEvent[] | null>(null);

  const reload = useCallback(() => {
    const gen = ++generation.current;
    during.current = [];
    // Project names are decoration: without them the rows still work.
    const projects = listProjects().then(
      (list) => list.items,
      () => null,
    );
    Promise.all([listSessions(), projects]).then(
      ([sessions, projectItems]) => {
        if (gen !== generation.current) return;
        const replay = during.current ?? [];
        during.current = null;
        setLists((prev) =>
          replay.reduce(applyAppEvent, {
            sessions: sessions.items,
            projects: projectItems ?? prev.projects,
          }),
        );
        setError(null);
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
    reload();
    return streamAppEvents({
      EventSource,
      onOpen: reload,
      onEvent: (e) => {
        during.current?.push(e);
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

  return { ...lists, loaded, error, setStatus };
}
