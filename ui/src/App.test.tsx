import { act, fireEvent, render, screen, within } from "@testing-library/react";

import { App } from "./App";
import type { Project, SessionSummary } from "./api/types.gen";
import { resources } from "./i18n/resources";
import { FakeEventSource } from "./test/fakeEventSource";
import { fixtureLines } from "./test/flowEvents";

const en = resources.en;

function summary(over: Partial<SessionSummary>): SessionSummary {
  return {
    id: "s-done",
    projectId: "p1",
    title: "Persist the sidebar width",
    provider: "claude",
    model: "claude-opus-5-5",
    mode: "accept_edits",
    status: "done",
    unseen: false,
    repos: ["scener"],
    branch: "feat/sidebar-width-pref",
    lastActivityAt: "2026-11-26T12:52:31Z",
    createdAt: "2026-11-26T12:49:00Z",
    ...over,
  };
}

const project: Project = {
  id: "p1",
  name: "scener",
  repos: [],
  sandbox: { state: "warm" },
  createdAt: "2026-11-26T10:00:00Z",
};

interface Server {
  sessions: SessionSummary[];
  projects: Project[];
  sessionsStatus?: number;
  calls: string[];
}

function serve(server: Server) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    server.calls.push(url);
    if (url === "/api/sessions") {
      if (server.sessionsStatus) {
        return new Response(JSON.stringify({ code: "sandbox_unavailable" }), {
          status: server.sessionsStatus,
        });
      }
      return new Response(JSON.stringify({ items: server.sessions }));
    }
    if (url === "/api/projects") {
      return new Response(JSON.stringify({ items: server.projects }));
    }
    return new Response(JSON.stringify({ code: "not_found" }), { status: 404 });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function emitDemo(es: FakeEventSource, upTo = Infinity) {
  act(() => {
    es.open();
    fixtureLines("demo.jsonl")
      .slice(0, upTo)
      .forEach((line) => es.emit("flow", line));
  });
}

describe("App", () => {
  beforeEach(() => {
    FakeEventSource.reset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the wordmark and the empty state from the resources", async () => {
    serve({ sessions: [], projects: [], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    expect(
      screen.getByRole("heading", { level: 1, name: en.common.appName }),
    ).toBeInTheDocument();
    expect(await screen.findByText(en.shell.empty)).toBeInTheDocument();
    expect(screen.getByText(en.shell.emptyHint)).toBeInTheDocument();
    expect(screen.getAllByText(en.sidebar.empty)).toHaveLength(4);
  });

  it("shows a list error from the errors namespace", async () => {
    serve({ sessions: [], projects: [], sessionsStatus: 503, calls: [] });
    render(<App EventSource={FakeEventSource} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.sandbox_unavailable,
    );
    expect(screen.queryByText(en.shell.empty)).toBeNull();
  });

  it("selects the most urgent session and renders its stream", async () => {
    const server: Server = {
      sessions: [
        summary({}),
        summary({
          id: "s-ask",
          title: "Fix the flaky migration test",
          status: "approval",
          branch: "fix/flaky",
          lastActivityAt: "2026-11-26T11:00:00Z",
        }),
      ],
      projects: [project],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);

    // Needs you comes first: the approval session is selected.
    const title = await screen.findByRole("heading", {
      level: 2,
      name: "Fix the flaky migration test",
    });
    const header = title.closest("header") as HTMLElement;
    expect(header).toHaveTextContent("scener");
    expect(header).toHaveTextContent("fix/flaky");
    const needs = screen.getByRole("region", { name: en.sidebar.sectionNeeds });
    expect(within(needs).getByRole("button")).toHaveAttribute(
      "aria-current",
      "true",
    );
    expect(within(needs).getByText(en.sidebar.statusApproval)).toBeVisible();
    const ask = FakeEventSource.forUrl("/api/sessions/s-ask/events");
    expect(ask).toHaveLength(1);

    // Switch to the done session: the old stream closes, a new one opens.
    const done = screen.getByRole("region", { name: en.sidebar.sectionDone });
    fireEvent.click(within(done).getByRole("button"));
    expect(ask[0].closed).toBe(true);
    const es = FakeEventSource.forUrl("/api/sessions/s-done/events")[0];
    emitDemo(es);

    expect(
      screen.getByText(/The sidebar width resets on every reload/),
    ).toBeInTheDocument();
    expect(screen.getByText("Worked for 3m 41s")).toBeInTheDocument();
    expect(screen.getByText("Nothing is pushed yet.")).toBeInTheDocument();
    expect(screen.getByText("3 files changed")).toBeInTheDocument();
    expect(screen.getByText("Allowed once")).toBeInTheDocument();
  });

  it("follows the selected session's status and the app stream", async () => {
    const server: Server = {
      sessions: [summary({ status: "idle" })],
      projects: [project],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    const es = FakeEventSource.forUrl("/api/sessions/s-done/events")[0];

    // Part way in, the turn runs: status working, stop button, indicator.
    emitDemo(es, 16);
    const working = screen.getByRole("region", {
      name: en.sidebar.sectionWorking,
    });
    expect(within(working).getByRole("button")).toHaveTextContent(
      en.sidebar.statusWorking,
    );
    expect(
      screen.getByRole("button", { name: en.composer.stop }),
    ).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(
      en.timeline.workingNow,
    );

    // The app stream adds and removes sessions; a reopen reloads the lists.
    const app = FakeEventSource.forUrl("/api/events")[0];
    act(() => {
      app.open();
      app.emit("app", {
        kind: "session.updated",
        session: summary({ id: "s-new", title: "Brand new", status: "idle" }),
      });
    });
    expect(await screen.findByText("Brand new")).toBeInTheDocument();
    act(() => {
      app.emit("app", { kind: "session.removed", sessionId: "s-new" });
      app.emit("app", {
        kind: "project.updated",
        project: { ...project, name: "scener-renamed" },
      });
    });
    expect(screen.queryByText("Brand new")).toBeNull();
    expect(screen.getAllByText("scener-renamed").length).toBeGreaterThan(0);
    act(() => {
      app.emit("app", { kind: "project.removed", projectId: "p1" });
      app.emit("app", { kind: "sandbox.updated" });
    });
    expect(screen.queryByText("scener-renamed")).toBeNull();

    const before = server.calls.filter((c) => c === "/api/sessions").length;
    server.sessions = [summary({ title: "Reloaded title", status: "done" })];
    act(() => app.open());
    expect(await screen.findAllByText("Reloaded title")).not.toHaveLength(0);
    expect(server.calls.filter((c) => c === "/api/sessions").length).toBe(
      before + 1,
    );
  });
});
