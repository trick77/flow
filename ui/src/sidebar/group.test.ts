import type { SessionStatus, SessionSummary } from "../api/types.gen";
import { SESSION_STATUS_VALUES } from "../api/types.gen";
import { groupSessions, orderSessions, sectionOf } from "./group";
import { applyAppEvent } from "./useSessions";

function s(id: string, status: SessionStatus, at: string): SessionSummary {
  return {
    id,
    projectId: "p",
    title: id,
    provider: "claude",
    model: "m",
    mode: "ask",
    status,
    unseen: false,
    repos: [],
    branch: "b",
    lastActivityAt: at,
    createdAt: at,
  };
}

describe("sidebar grouping", () => {
  it("puts every status into its section", () => {
    const got = Object.fromEntries(
      SESSION_STATUS_VALUES.map((st) => [st, sectionOf(st)]),
    );
    expect(got).toEqual({
      idle: "idle",
      working: "working",
      queued: "working",
      approval: "needs",
      plan: "needs",
      question: "needs",
      done: "done",
      failed: "done",
    });
    expect(sectionOf("future" as SessionStatus)).toBe("idle");
  });

  it("sorts each section by latest activity and orders the sections", () => {
    const list = [
      s("done-old", "done", "2026-11-26T10:00:00Z"),
      s("idle", "idle", "2026-11-26T13:00:00Z"),
      s("ask", "approval", "2026-11-26T09:00:00Z"),
      s("done-new", "failed", "2026-11-26T12:00:00Z"),
      s("run", "queued", "2026-11-26T08:00:00Z"),
      s("b-tie", "question", "2026-11-26T09:00:00Z"),
    ];
    const g = groupSessions(list);
    expect(g.needs.map((x) => x.id)).toEqual(["ask", "b-tie"]);
    expect(g.done.map((x) => x.id)).toEqual(["done-new", "done-old"]);
    expect(orderSessions(list).map((x) => x.id)).toEqual([
      "ask",
      "b-tie",
      "run",
      "done-new",
      "done-old",
      "idle",
    ]);
  });
});

describe("applyAppEvent", () => {
  const base = {
    sessions: [s("a", "idle", "2026-11-26T10:00:00Z")],
    projects: [],
  };

  it("leaves the lists alone for events without a payload or other kinds", () => {
    expect(applyAppEvent(base, { kind: "session.updated" })).toBe(base);
    expect(applyAppEvent(base, { kind: "project.updated" })).toBe(base);
    expect(applyAppEvent(base, { kind: "connections.updated" })).toBe(base);
  });

  it("replaces a known session in place", () => {
    const next = applyAppEvent(base, {
      kind: "session.updated",
      session: { ...base.sessions[0], title: "renamed" },
    });
    expect(next.sessions).toHaveLength(1);
    expect(next.sessions[0].title).toBe("renamed");
  });
});
