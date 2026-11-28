import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";

import { App } from "./App";
import type { Project, SessionSummary } from "./api/types.gen";
import { resources } from "./i18n/resources";
import { FakeWebSocket } from "./terminal/fakeSocket";
import { FakeEventSource } from "./test/fakeEventSource";
import { fixtureLines, mk } from "./test/flowEvents";

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
  projectsStatus?: number;
  // archive answers DELETE /api/sessions/{id}: 204 or an error code.
  archive?: string[];
  // created answers POST /api/sessions, once createGate (if any) settles.
  created?: SessionSummary;
  createGate?: Promise<void>;
  // patchCode refuses PATCH /api/sessions/{id}; else it answers the change.
  patchCode?: string;
  // extra answers a call before the rest (a test's own routes).
  extra?: (url: string, init?: RequestInit) => Response | undefined;
  calls: string[];
}

function serve(server: Server) {
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      server.calls.push(init?.method === "DELETE" ? "DELETE " + url : url);
      const own = server.extra?.(url, init);
      if (own) return own;
      if (init?.method === "DELETE" && url.startsWith("/api/sessions/")) {
        const code = server.archive?.shift();
        return code
          ? new Response(JSON.stringify({ code }), { status: 409 })
          : new Response(null, { status: 204 });
      }
      if (init?.method === "PATCH" && url.startsWith("/api/sessions/")) {
        const body = String(init.body);
        server.calls.push(`PATCH ${url} ${body}`);
        if (server.patchCode) {
          return new Response(JSON.stringify({ code: server.patchCode }), {
            status: 501,
          });
        }
        const id = decodeURIComponent(url.slice("/api/sessions/".length));
        const s = server.sessions.find((x) => x.id === id);
        return new Response(
          JSON.stringify({
            summary: { ...s, ...JSON.parse(body) },
            dir: "/w",
            worktrees: [],
            lastSeq: 0,
          }),
        );
      }
      if (init?.method === "POST" && url.endsWith("/seen")) {
        server.calls.push(`SEEN ${url} ${String(init.body)}`);
        return new Response(null, { status: 204 });
      }
      if (url === "/api/agents") {
        return new Response(
          JSON.stringify({
            providers: [
              {
                provider: "claude",
                available: true,
                claudeAuths: ["subscription_token"],
                models: [{ id: "claude-opus-5-5", label: "Opus 5.5" }],
                defaultModel: "claude-opus-5-5",
              },
            ],
            modes: ["plan", "ask"],
          }),
        );
      }
      if (
        url === "/api/sessions" &&
        init?.method === "POST" &&
        server.created
      ) {
        server.calls.push("POST /api/sessions");
        await server.createGate;
        return new Response(
          JSON.stringify({
            summary: server.created,
            dir: "/w",
            worktrees: [],
            lastSeq: 0,
          }),
          { status: 201 },
        );
      }
      if (url === "/api/terminals") {
        return new Response(
          JSON.stringify({ id: "t1", projectId: "p1", cwd: "/w" }),
          { status: 201 },
        );
      }
      if (url === "/api/projects" && server.projectsStatus) {
        return new Response(JSON.stringify({ code: "internal" }), {
          status: server.projectsStatus,
        });
      }
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
      if (init?.method === "POST" && url.endsWith("/sandbox/start")) {
        return new Response(JSON.stringify({ state: "setup" }), {
          status: 202,
        });
      }
      return new Response(JSON.stringify({ code: "not_found" }), {
        status: 404,
      });
    },
  );
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

// The heading comes from the session list; the stream opens in a later
// effect, so a lookup right after the heading can find nothing under load.
async function doneStream(): Promise<FakeEventSource> {
  const url = "/api/sessions/s-done/events";
  await waitFor(() => expect(FakeEventSource.forUrl(url)).not.toHaveLength(0));
  return FakeEventSource.forUrl(url)[0];
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

  it("opens New session on a project's + and on Add a project the projects page", async () => {
    window.localStorage.clear();
    const created = summary({ id: "s-new", projectId: "p2", title: "Go" });
    const server: Server = {
      sessions: [],
      projects: [
        project,
        {
          ...project,
          id: "p2",
          name: "ovh-restock",
          repos: [
            { id: "r9", fullName: "dev/ovh-restock", defaultBranch: "main" },
          ],
        },
      ],
      created,
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByText(en.shell.empty);
    fireEvent.click(
      screen.getByRole("radio", { name: en.sidebar.groupByProject }),
    );
    // A project without sessions is listed, its + starts one in it.
    fireEvent.click(
      screen.getByRole("button", { name: "New session in ovh-restock" }),
    );
    const box = await screen.findByLabelText(en.composer.startPlaceholder);
    expect(
      screen.getByText("What should we work on in ovh-restock?"),
    ).toBeInTheDocument();
    expect(screen.getByText("Working in dev/ovh-restock")).toBeInTheDocument();
    // Another +, with a draft typed: the draft moves along.
    fireEvent.change(box, { target: { value: "Go" } });
    fireEvent.click(
      screen.getByRole("button", { name: "New session in scener" }),
    );
    expect(
      screen.getByText("What should we work on in scener?"),
    ).toBeInTheDocument();
    expect(box).toHaveValue("Go");
    fireEvent.click(
      screen.getByRole("button", { name: "New session in ovh-restock" }),
    );
    fireEvent.keyDown(box, { key: "Enter" });
    expect(
      await screen.findByRole("heading", { level: 2, name: "Go" }),
    ).toBeInTheDocument();
    expect(server.calls).toContain("POST /api/sessions");
  });

  it("points to the projects page from the sidebar without any project", async () => {
    window.localStorage.clear();
    serve({ sessions: [], projects: [], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByText(en.shell.empty);
    fireEvent.click(
      screen.getByRole("radio", { name: en.sidebar.groupByProject }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: en.sidebar.addProject }),
    );
    expect(
      screen.getByRole("heading", { level: 1, name: en.projects.title }),
    ).toBeInTheDocument();
  });

  it("does not say there is no project when the session list fails", async () => {
    window.localStorage.clear();
    serve({ sessions: [], projects: [], sessionsStatus: 500, calls: [] });
    render(<App EventSource={FakeEventSource} />);
    fireEvent.click(
      screen.getByRole("radio", { name: en.sidebar.groupByProject }),
    );
    expect(
      await within(
        screen.getByRole("navigation", { name: en.sidebar.listLabel }),
      ).findByRole("alert"),
    ).toBeInTheDocument();
    expect(screen.queryByText(en.sidebar.noProjects)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.sidebar.addProject }),
    ).not.toBeInTheDocument();
  });

  it("starts a session from the sidebar and opens it at once", async () => {
    window.localStorage.clear();
    const created = summary({
      id: "s-new",
      title: "Clamp the width",
      status: "working",
      branch: "flow/clamp-the-width",
      lastActivityAt: "2026-11-26T09:00:00Z",
    });
    const server: Server = {
      sessions: [summary({})],
      projects: [
        {
          ...project,
          repos: [
            { id: "r1", fullName: "dev/scener", defaultBranch: "master" },
          ],
        },
      ],
      created,
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    // The composer names the model from the catalog, as a picker.
    expect(
      await screen.findByRole("button", {
        name: "Model: Claude Code, Opus 5.5",
      }),
    ).toBeInTheDocument();

    const newButton = screen.getByRole("button", {
      name: en.sidebar.newSession,
    });
    fireEvent.click(newButton);
    expect(newButton).toHaveAttribute("aria-current", "page");
    const box = await screen.findByLabelText(en.composer.startPlaceholder);
    expect(
      screen.getByText("What should we work on in scener?"),
    ).toBeInTheDocument();
    fireEvent.change(box, { target: { value: "Clamp the width" } });
    fireEvent.keyDown(box, { key: "Enter" });

    expect(
      await screen.findByRole("heading", { level: 2, name: "Clamp the width" }),
    ).toBeInTheDocument();
    expect(server.calls).toContain("POST /api/sessions");
    expect(newButton).not.toHaveAttribute("aria-current");
    const row = within(
      screen.getByRole("navigation", { name: en.sidebar.listLabel }),
    ).getByRole("button", { name: /Clamp the width/ });
    expect(row).toHaveAttribute("aria-current", "true");
    expect(FakeEventSource.forUrl("/api/sessions/s-new/events")).toHaveLength(
      1,
    );
  });

  it("offers New session on the empty start", async () => {
    serve({ sessions: [], projects: [], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    fireEvent.click(
      await screen.findByRole("button", { name: en.shell.newSession }),
    );
    expect(screen.getByRole("status")).toHaveTextContent(en.composer.noProject);
    fireEvent.click(
      screen.getByRole("button", { name: en.composer.openProjects }),
    );
    expect(
      screen.getByRole("heading", { level: 1, name: en.projects.title }),
    ).toBeInTheDocument();
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
        // Done while unseen: a seen done session sits in Idle.
        summary({ unseen: true }),
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
    const es = await doneStream();
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
    const es = await doneStream();

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

  it("leaves the sidebar status to the row for statuses it mirrored already", async () => {
    // A turn cut off by a sandbox stop: the row says failed up to seq 16, the
    // log still says working there.
    serve({
      sessions: [summary({ status: "failed", lastSeq: 16, unseen: true })],
      projects: [project],
      calls: [],
    });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    const es = await doneStream();
    emitDemo(es, 16);
    const row = () =>
      within(
        screen.getByRole("navigation", { name: en.sidebar.listLabel }),
      ).getByRole("button", { name: /Persist the sidebar width/ });
    expect(row()).toHaveTextContent(en.sidebar.statusFailed);
    // A status past the row's last seq is news.
    act(() => es.emit("flow", fixtureLines("demo.jsonl")[17]));
    expect(row()).toHaveTextContent(en.sidebar.statusApproval);
  });

  it("says the session's sandbox is stopped and starts it from there", async () => {
    const server: Server = {
      sessions: [summary({ status: "failed" })],
      projects: [{ ...project, sandbox: { state: "stopped" } }],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    expect(await screen.findByText(en.shell.sandboxStopped)).toBeVisible();
    // A log that ends mid-turn (the stop wrote no turn.ended) shows no running
    // turn: the row, where Flow ended it, wins while the sandbox is down.
    emitDemo(await doneStream(), 16);
    expect(screen.queryByRole("button", { name: en.composer.stop })).toBeNull();
    expect(screen.queryByText(en.timeline.workingNow)).toBeNull();

    fireEvent.click(
      screen.getByRole("button", { name: en.shell.startSandbox }),
    );
    expect(server.calls).toContain("/api/projects/p1/sandbox/start");
    expect(screen.getByText(en.shell.sandboxStarting)).toBeVisible();
    expect(
      screen.queryByRole("button", { name: en.shell.startSandbox }),
    ).toBeNull();

    const app = FakeEventSource.forUrl("/api/events")[0];
    act(() => {
      app.emit("app", {
        kind: "sandbox.updated",
        projectId: "p1",
        sandbox: { state: "warm" },
      });
    });
    expect(screen.queryByText(en.shell.sandboxStarting)).toBeNull();
    expect(screen.queryByText(en.shell.sandboxStopped)).toBeNull();
  });

  it("opens Projects and Settings from the header, also without sessions", async () => {
    serve({ sessions: [], projects: [project], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByText(en.shell.empty);
    fireEvent.click(screen.getByRole("button", { name: en.shell.projects }));
    expect(
      screen.getByRole("heading", { level: 1, name: en.projects.title }),
    ).toBeInTheDocument();
    expect(screen.getByRole("article", { name: "scener" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: en.shell.projects }),
    ).toHaveAttribute("aria-current", "page");
    // No session to go back to.
    expect(
      screen.queryByRole("button", { name: en.shell.backToSession }),
    ).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: en.shell.settings }));
    expect(
      screen.getByRole("heading", { level: 1, name: en.settings.title }),
    ).toBeInTheDocument();
    // The current page's button goes back.
    fireEvent.click(screen.getByRole("button", { name: en.shell.settings }));
    expect(screen.getByText(en.shell.empty)).toBeInTheDocument();
  });

  it("says the projects failed to load instead of showing none", async () => {
    serve({
      sessions: [],
      projects: [project],
      projectsStatus: 500,
      calls: [],
    });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByText(en.shell.empty);
    fireEvent.click(screen.getByRole("button", { name: en.shell.projects }));
    expect(screen.getByRole("alert")).toHaveTextContent(en.projects.loadFailed);
    expect(screen.queryByText(en.projects.empty)).toBeNull();
  });

  it("keeps the session's draft, terminal and stream across a page", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    // xterm asks for the device pixel ratio's media query; jsdom has none.
    vi.stubGlobal(
      "matchMedia",
      () =>
        ({ matches: false, addListener() {}, removeListener() {} }) as unknown,
    );
    FakeWebSocket.reset();
    serve({ sessions: [summary({})], projects: [project], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    fireEvent.change(screen.getByLabelText(en.composer.placeholder), {
      target: { value: "half a message" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.terminal.toggle }));
    });
    fireEvent.click(screen.getByRole("button", { name: en.shell.settings }));
    expect(
      screen.getByRole("heading", { level: 1, name: en.settings.title }),
    ).toBeInTheDocument();
    // The session is out of view (hidden), not gone.
    expect(screen.queryByRole("region", { name: en.shell.session })).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: en.shell.backToSession }),
    );
    expect(screen.getByLabelText(en.composer.placeholder)).toHaveValue(
      "half a message",
    );
    expect(
      screen.getByRole("button", { name: en.terminal.toggle }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(FakeEventSource.forUrl("/api/sessions/s-done/events")).toHaveLength(
      1,
    );
    expect(
      FakeEventSource.forUrl("/api/sessions/s-done/events")[0].closed,
    ).toBe(false);
    // The terminal's one socket, still open.
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(FakeWebSocket.instances[0].closedWith).toBeUndefined();
  });

  it("archives the open session after asking, and says why it could not", async () => {
    const server: Server = {
      sessions: [summary({}), summary({ id: "s2", title: "Second one" })],
      projects: [project],
      archive: ["sandbox_unavailable"],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", {
      level: 2,
      name: "Persist the sidebar width",
    });
    fireEvent.click(screen.getByRole("button", { name: en.shell.archive }));
    const ask = screen.getByRole("alertdialog", { name: en.shell.archive });
    expect(ask).toHaveTextContent("Archive Persist the sidebar width?");
    expect(ask).toHaveTextContent("uncommitted");
    fireEvent.click(within(ask).getByRole("button", { name: en.shell.cancel }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(server.calls.filter((c) => c.startsWith("DELETE"))).toHaveLength(0);

    const confirm = async () => {
      await act(async () => {
        fireEvent.click(
          screen.getByRole("button", { name: en.shell.confirmArchiveAction }),
        );
      });
    };
    fireEvent.click(screen.getByRole("button", { name: en.shell.archive }));
    await confirm();
    expect(screen.getByRole("alert")).toHaveTextContent(
      en.errors.sandbox_unavailable,
    );
    await confirm();
    expect(server.calls).toContain("DELETE /api/sessions/s-done");
    // Gone from the sidebar; the other session shows.
    expect(
      await screen.findByRole("heading", { level: 2, name: "Second one" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Persist the sidebar width")).toBeNull();
  });

  it("keeps the diff panel and Archive in one menu on phones", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: [
        "(max-width: 420px)",
        "(max-width: 900px)",
        "(max-width: 1180px)",
      ].includes(q),
      addEventListener() {},
      removeEventListener() {},
    }));
    const server: Server = {
      sessions: [summary({})],
      projects: [project],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", {
      level: 2,
      name: "Persist the sidebar width",
    });
    expect(screen.queryByRole("button", { name: en.shell.archive })).toBeNull();
    expect(
      screen.queryByRole("button", { name: en.shell.togglePanel }),
    ).toBeNull();
    const more = screen.getByRole("button", { name: en.shell.more });
    expect(more).toHaveAttribute("aria-haspopup", "menu");

    // Keyboard: the first item takes the focus, Down moves, Escape closes.
    fireEvent.click(more);
    const menu = screen.getByRole("menu", { name: en.shell.moreMenu });
    const panelItem = within(menu).getByRole("menuitemcheckbox", {
      name: en.shell.panelItem,
    });
    expect(panelItem).toHaveFocus();
    expect(panelItem).toHaveAttribute("aria-checked", "false");
    fireEvent.keyDown(panelItem, { key: "ArrowDown" });
    const archiveItem = within(menu).getByRole("menuitem", {
      name: en.shell.archive,
    });
    expect(archiveItem).toHaveFocus();
    fireEvent.keyDown(archiveItem, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    expect(more).toHaveFocus();

    // Archive from the menu asks first, as the header button did.
    fireEvent.click(more);
    fireEvent.click(screen.getByRole("menuitem", { name: en.shell.archive }));
    expect(screen.queryByRole("menu")).toBeNull();
    expect(more).toHaveFocus();
    const ask = screen.getByRole("alertdialog", { name: en.shell.archive });
    expect(ask).toHaveTextContent("Archive Persist the sidebar width?");
    await act(async () => {
      fireEvent.click(
        within(ask).getByRole("button", {
          name: en.shell.confirmArchiveAction,
        }),
      );
    });
    expect(server.calls).toContain("DELETE /api/sessions/s-done");
  });

  it("opens the diff panel from the phone menu", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: [
        "(max-width: 420px)",
        "(max-width: 900px)",
        "(max-width: 1180px)",
      ].includes(q),
      addEventListener() {},
      removeEventListener() {},
    }));
    serve({ sessions: [summary({})], projects: [project], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", {
      level: 2,
      name: "Persist the sidebar width",
    });
    const more = screen.getByRole("button", { name: en.shell.more });
    fireEvent.click(more);
    await act(async () => {
      fireEvent.click(
        screen.getByRole("menuitemcheckbox", { name: en.shell.panelItem }),
      );
    });
    expect(document.getElementById("review-panel")).not.toBeNull();
    fireEvent.click(more);
    expect(
      screen.getByRole("menuitemcheckbox", { name: en.shell.panelItem }),
    ).toHaveAttribute("aria-checked", "true");
  });

  it("goes back to the session from a page, and to a session picked in the sidebar", async () => {
    serve({
      sessions: [summary({}), summary({ id: "s2", title: "Second one" })],
      projects: [project],
      calls: [],
    });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", {
      level: 2,
      name: "Persist the sidebar width",
    });
    fireEvent.click(screen.getByRole("button", { name: en.shell.projects }));
    fireEvent.click(
      screen.getByRole("button", { name: en.shell.backToSession }),
    );
    expect(
      screen.queryByRole("heading", { level: 1, name: en.projects.title }),
    ).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: en.shell.settings }));
    // Seen done sessions sit in Idle.
    const sidebar = screen.getByRole("region", {
      name: en.sidebar.sectionIdle,
    });
    fireEvent.click(within(sidebar).getByText("Second one"));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Second one" }),
    ).toBeInTheDocument();
  });

  const withRepo: Project = {
    ...project,
    repos: [{ id: "r1", fullName: "dev/scener", defaultBranch: "master" }],
  };

  it("keeps the New session draft while another view shows", async () => {
    window.localStorage.clear();
    serve({ sessions: [summary({})], projects: [withRepo], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    fireEvent.click(
      screen.getByRole("button", { name: en.sidebar.newSession }),
    );
    fireEvent.change(
      await screen.findByLabelText(en.composer.startPlaceholder),
      { target: { value: "half a task" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: en.shell.settings })[0],
    );
    expect(
      screen.getByRole("heading", { level: 1, name: en.settings.title }),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: en.sidebar.newSession }),
    );
    expect(screen.getByLabelText(en.composer.startPlaceholder)).toHaveValue(
      "half a task",
    );
  });

  it("lists a session started from a hidden New session without switching to it", async () => {
    window.localStorage.clear();
    let open!: () => void;
    const server: Server = {
      sessions: [summary({}), summary({ id: "s2", title: "Second one" })],
      projects: [withRepo],
      created: summary({
        id: "s-new",
        title: "Clamp the width",
        status: "working",
      }),
      createGate: new Promise((r) => (open = r)),
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    fireEvent.click(
      screen.getByRole("button", { name: en.sidebar.newSession }),
    );
    const box = await screen.findByLabelText(en.composer.startPlaceholder);
    fireEvent.change(box, { target: { value: "Clamp the width" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(server.calls).toContain("POST /api/sessions"));
    // Another session is opened while the start runs.
    const list = screen.getByRole("navigation", { name: en.sidebar.listLabel });
    fireEvent.click(within(list).getByText("Second one"));
    await act(async () => open());
    expect(
      await within(list).findByRole("button", { name: /Clamp the width/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: "Second one" }),
    ).toBeInTheDocument();
    // The next New session starts empty.
    fireEvent.click(
      screen.getByRole("button", { name: en.sidebar.newSession }),
    );
    expect(
      await screen.findByLabelText(en.composer.startPlaceholder),
    ).toHaveValue("");
  });

  it("renames the open session from its header", async () => {
    const server: Server = {
      sessions: [summary({})],
      projects: [project],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    const title = await screen.findByRole("button", {
      name: "Persist the sidebar width",
      description: en.shell.rename,
    });
    fireEvent.click(title);
    const field = screen.getByRole("textbox", { name: en.shell.renameLabel });
    fireEvent.change(field, { target: { value: "  Clamp the width  " } });
    fireEvent.keyDown(field, { key: "Enter" });
    // Shown at once, kept from the answer.
    expect(
      screen.getByRole("heading", { level: 2, name: "Clamp the width" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(server.calls).toContain(
        'PATCH /api/sessions/s-done {"title":"Clamp the width"}',
      ),
    );
    const list = screen.getByRole("navigation", { name: en.sidebar.listLabel });
    expect(
      await within(list).findByText("Clamp the width"),
    ).toBeInTheDocument();
  });

  it("puts the old title back when a rename is refused", async () => {
    serve({
      sessions: [summary({})],
      projects: [project],
      patchCode: "not_implemented",
      calls: [],
    });
    render(<App EventSource={FakeEventSource} />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Persist the sidebar width" }),
    );
    const field = screen.getByRole("textbox", { name: en.shell.renameLabel });
    fireEvent.change(field, { target: { value: "Other" } });
    fireEvent.blur(field);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.shell.renameFailed);
    expect(alert).toHaveTextContent(en.errors.not_implemented);
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: "Persist the sidebar width",
      }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: en.shell.dismiss }));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("hands a waiting question to the composer", async () => {
    serve({ sessions: [summary({})], projects: [project], calls: [] });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    const es = await doneStream();
    act(() => {
      es.open();
      es.emit(
        "flow",
        mk("turn.started", { messageId: "m" }, { seq: 1, turn: "t" }),
      );
      es.emit(
        "flow",
        mk(
          "question.asked",
          {
            requestId: "q",
            questions: [
              {
                id: "a",
                text: "Which key?",
                multiSelect: false,
                options: [{ id: "o", label: "SHA" }],
              },
            ],
          },
          { seq: 2, turn: "t" },
        ),
      );
    });
    fireEvent.click(
      await screen.findByRole("button", { name: en.timeline.answerInComposer }),
    );
    expect(screen.getByLabelText(en.composer.answerPlaceholder)).toHaveFocus();
  });

  it("marks the open session seen and clears its bold title", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const server: Server = {
      sessions: [summary({ unseen: true })],
      projects: [project],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    const list = await screen.findByRole("navigation", {
      name: en.sidebar.listLabel,
    });
    const row = await within(list).findByRole("button", {
      name: /Persist the sidebar width/,
    });
    expect(row).toHaveClass("unseen");
    emitDemo(await doneStream(), 3);
    await waitFor(
      () =>
        expect(server.calls).toContain(
          'SEEN /api/sessions/s-done/seen {"seq":3}',
        ),
      { timeout: 3000 },
    );
    // Seen, the done row moves from Done to Idle: a new element there.
    await waitFor(() =>
      expect(
        within(
          screen.getByRole("region", { name: en.sidebar.sectionIdle }),
        ).getByRole("button", { name: /Persist the sidebar width/ }),
      ).not.toHaveClass("unseen"),
    );
    vi.restoreAllMocks();
  });

  it("marks a session cut off by a sandbox stop seen with the row's last seq", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const server: Server = {
      sessions: [summary({ unseen: true, status: "failed", lastSeq: 42 })],
      projects: [{ ...project, sandbox: { state: "stopped" } }],
      calls: [],
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    const list = await screen.findByRole("navigation", {
      name: en.sidebar.listLabel,
    });
    const row = await within(list).findByRole("button", {
      name: /Persist the sidebar width/,
    });
    expect(row).toHaveClass("unseen");
    // The stream of a stopped sandbox delivers nothing.
    await waitFor(
      () =>
        expect(server.calls).toContain(
          'SEEN /api/sessions/s-done/seen {"seq":42}',
        ),
      { timeout: 3000 },
    );
    // Seen, the done row moves from Done to Idle: a new element there.
    await waitFor(() =>
      expect(
        within(
          screen.getByRole("region", { name: en.sidebar.sectionIdle }),
        ).getByRole("button", { name: /Persist the sidebar width/ }),
      ).not.toHaveClass("unseen"),
    );
    vi.restoreAllMocks();
  });

  it("opens the review panel from the header and the repos menu, again after a turn", async () => {
    const worktrees = [
      {
        repoId: "r1",
        repo: "dev/scener",
        branch: "b",
        path: "/w/scener",
        baseRef: "origin/master",
      },
      {
        repoId: "r2",
        repo: "dev/helpers",
        branch: "b",
        path: "/w/helpers",
        baseRef: "origin/master",
      },
    ];
    const diffBody = (repo: string) =>
      JSON.stringify({
        repo,
        baseRef: "origin/master",
        baseSha: "a",
        files: [],
      });
    const server: Server = {
      sessions: [summary({ status: "idle", repos: ["scener", "helpers"] })],
      projects: [project],
      calls: [],
      extra: (url) => {
        if (url === "/api/sessions/s-done") {
          return new Response(
            JSON.stringify({
              summary: summary({}),
              dir: "/w",
              worktrees,
              lastSeq: 0,
            }),
          );
        }
        const m = url.match(/^\/api\/sessions\/s-done\/repos\/(r\d)\/diff$/);
        if (m)
          return new Response(
            diffBody(m[1] === "r1" ? "dev/scener" : "dev/helpers"),
          );
        return undefined;
      },
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    expect(screen.getByTitle(en.shell.sandboxChipWarm)).toBeInTheDocument();
    const toggle = screen.getByRole("button", { name: en.shell.togglePanel });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    // Closed, the panel reads no diff.
    expect(server.calls.some((c) => c.endsWith("/diff"))).toBe(false);

    // The repos menu opens the panel on the chosen repository.
    fireEvent.click(
      await screen.findByRole("button", {
        name: "2 repositories in this session",
      }),
    );
    fireEvent.click(
      await screen.findByRole("menuitem", { name: /dev\/helpers/ }),
    );
    expect(
      await screen.findByText("No changes against origin/master yet."),
    ).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    expect(
      within(
        screen.getByRole("radiogroup", { name: en.panel.repository }),
      ).getByRole("radio", { name: "helpers" }),
    ).toHaveAttribute("aria-checked", "true");
    const diffs = () =>
      server.calls.filter((c) => c === "/api/sessions/s-done/repos/r2/diff")
        .length;
    const before = diffs();

    // A turn that ends reads the diff again.
    const es = await doneStream();
    emitDemo(es, 16);
    act(() => {
      fixtureLines("demo.jsonl")
        .slice(16)
        .forEach((line) => es.emit("flow", line));
    });
    await waitFor(() => expect(diffs()).toBeGreaterThan(before));

    fireEvent.click(screen.getByRole("button", { name: en.panel.close }));
    expect(
      screen.queryByRole("complementary", { name: en.panel.label }),
    ).toBeNull();
    expect(toggle).toHaveFocus();
    fireEvent.click(toggle);
    expect(
      screen.getByRole("complementary", { name: en.panel.label }),
    ).toBeInTheDocument();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "false");
  });

  it("groups as the profile says and reads it again on profile.updated", async () => {
    window.localStorage.clear();
    let grouping = "status";
    const server: Server = {
      sessions: [summary({})],
      projects: [project],
      calls: [],
      extra: (url, init) =>
        url === "/api/profile" && (init?.method ?? "GET") === "GET"
          ? new Response(
              JSON.stringify({
                gitName: "",
                gitEmail: "",
                language: "en",
                formatLocale: "en-CH",
                timeZone: "Europe/Zurich",
                sidebarGrouping: grouping,
                configSync: { state: "never" },
              }),
            )
          : undefined,
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    const byProject = screen.getByRole("radio", {
      name: en.sidebar.groupByProject,
    });
    expect(byProject).toHaveAttribute("aria-checked", "false");
    // Another tab changed it: the frame carries no profile, so it is read.
    grouping = "project";
    act(() => {
      const app = FakeEventSource.forUrl("/api/events")[0];
      app.open();
      app.emit("app", { kind: "profile.updated" });
    });
    await waitFor(() =>
      expect(byProject).toHaveAttribute("aria-checked", "true"),
    );
    expect(
      screen.getByRole("region", { name: project.name }),
    ).toBeInTheDocument();
    expect(
      server.calls.filter((c) => c === "/api/profile").length,
    ).toBeGreaterThanOrEqual(2);
  });

  it("reads the connections again on connections.updated", async () => {
    let github: Record<string, string> = { state: "disconnected" };
    const server: Server = {
      sessions: [summary({})],
      projects: [project],
      calls: [],
      extra: (url) => {
        if (url === "/api/profile") {
          return new Response(
            JSON.stringify({
              gitName: "",
              gitEmail: "",
              language: "en",
              formatLocale: "en-CH",
              timeZone: "Europe/Zurich",
              sidebarGrouping: "status",
              configSync: { state: "never" },
            }),
          );
        }
        if (url === "/api/connections") {
          return new Response(
            JSON.stringify({
              github,
              claude: { signedIn: false },
              apiKeys: [],
            }),
          );
        }
        return undefined;
      },
    };
    serve(server);
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.shell.settings }));
    });
    const row = await screen.findByRole("group", { name: en.settings.github });
    expect(row).toHaveTextContent(en.settings.pillNotConnected);
    // Connected in another tab: the frame carries nothing, so it is read.
    github = { state: "connected", login: "octo-user" };
    act(() => {
      const app = FakeEventSource.forUrl("/api/events")[0];
      app.open();
      app.emit("app", { kind: "connections.updated" });
    });
    await waitFor(() => expect(row).toHaveTextContent("octo-user"));
  });

  it("remembers the rail and makes the sidebar a drawer on narrow screens", async () => {
    window.localStorage.clear();
    serve({
      sessions: [summary({}), summary({ id: "s2", title: "Second one" })],
      projects: [project],
      calls: [],
    });
    const { unmount } = render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    fireEvent.click(screen.getByRole("button", { name: en.sidebar.collapse }));
    expect(
      screen.getByRole("navigation", { name: en.sidebar.railLabel }),
    ).toBeInTheDocument();
    expect(window.localStorage.getItem("flow.sidebar.rail")).toBe("1");
    unmount();

    // Narrow: the rail does not apply, the menu button opens the drawer.
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q === "(max-width: 900px)" || q === "(max-width: 1180px)",
      addEventListener() {},
      removeEventListener() {},
    }));
    const raf = vi
      .spyOn(window, "requestAnimationFrame")
      .mockImplementation((fn) => {
        fn(0);
        return 0;
      });
    render(<App EventSource={FakeEventSource} />);
    await screen.findByRole("heading", { level: 2, name: summary({}).title });
    expect(
      screen.queryByRole("navigation", { name: en.sidebar.railLabel }),
    ).toBeNull();
    const menu = screen.getByRole("button", { name: en.shell.openSidebar });
    fireEvent.click(menu);
    const dialog = screen.getByRole("dialog", { name: en.sidebar.label });
    expect(menu).toHaveAttribute("aria-expanded", "true");
    fireEvent.keyDown(
      within(dialog).getByRole("button", { name: en.sidebar.closeSidebar }),
      { key: "Escape" },
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    // The scrim closes it too; choosing a session closes it and shows it.
    fireEvent.click(menu);
    fireEvent.click(document.querySelector(".scrim") as Element);
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(menu);
    fireEvent.click(within(screen.getByRole("dialog")).getByText("Second one"));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(
      await screen.findByRole("heading", { level: 2, name: "Second one" }),
    ).toBeInTheDocument();
    raf.mockRestore();
  });
});
