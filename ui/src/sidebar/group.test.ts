import type { SessionStatus, SessionSummary } from "../api/types.gen";
import { SESSION_STATUS_VALUES } from "../api/types.gen";
import { groupSessions, orderSessions, sectionOf } from "./group";
import { applyAnswer, applyAppEvent } from "./useSessions";

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

  it("puts a sandbox's new state on its project", () => {
    const p = (id: string) => ({
      id,
      name: id,
      repos: [],
      sandbox: { state: "setup" as const },
      createdAt: "2026-11-26T10:00:00Z",
    });
    const lists = { sessions: [], projects: [p("p1"), p("p2")] };
    expect(
      applyAppEvent(lists, { kind: "sandbox.updated", projectId: "p1" }),
    ).toBe(lists);
    const next = applyAppEvent(lists, {
      kind: "sandbox.updated",
      projectId: "p1",
      sandbox: { state: "warm" },
    });
    expect(next.projects.map((x) => x.sandbox.state)).toEqual([
      "warm",
      "setup",
    ]);
  });
});

describe("applyAnswer", () => {
  const p = (state: "setup" | "failed") => ({
    id: "p1",
    name: "apps",
    repos: [],
    sandbox: { state },
    createdAt: "2026-11-26T10:00:00Z",
  });

  it("never sets a project back to an answer's older state", () => {
    // The stream showed failed before the creation's answer (setup) came back.
    const lists = { sessions: [], projects: [p("failed")] };
    expect(
      applyAnswer(lists, {
        kind: "project.updated",
        projectId: "p1",
        project: p("setup"),
      }),
    ).toBe(lists);
    expect(
      applyAnswer(lists, {
        kind: "sandbox.updated",
        projectId: "p1",
        sandbox: { state: "setup" },
      }),
    ).toBe(lists);
  });

  it("adds a created project and applies removals", () => {
    const empty = {
      sessions: [s("a", "idle", "2026-11-26T10:00:00Z")],
      projects: [],
    };
    const added = applyAnswer(empty, {
      kind: "project.updated",
      projectId: "p1",
      project: p("setup"),
    });
    expect(added.projects.map((x) => x.id)).toEqual(["p1"]);
    expect(
      applyAnswer(added, { kind: "project.removed", projectId: "p1" }).projects,
    ).toEqual([]);
    expect(
      applyAnswer(added, { kind: "session.removed", sessionId: "a" }).sessions,
    ).toEqual([]);
  });
});
