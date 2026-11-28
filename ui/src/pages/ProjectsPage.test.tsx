import { act, fireEvent, render, screen, within } from "@testing-library/react";

import type { AppEvent, Project, Sandbox } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { ProjectsPage } from "./ProjectsPage";

const en = resources.en.projects;
const errors = resources.en.errors;

function project(over: Partial<Project> = {}, sandbox?: Sandbox): Project {
  return {
    id: "p1",
    name: "apps",
    repos: [
      { id: "r1", fullName: "trick77/toolkit", defaultBranch: "master" },
      { id: "r2", fullName: "trick77/ship", defaultBranch: "master" },
    ],
    sandbox: sandbox ?? {
      state: "warm",
      lastUsedAt: "2026-11-26T13:05:00Z",
      flowdVersion: "0.1.0-abc",
    },
    createdAt: "2026-11-26T10:00:00Z",
    ...over,
  };
}

interface Call {
  method: string;
  url: string;
  body?: unknown;
}

// serve answers each call with the next queued response.
function serve(answers: Response[]) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({
        method: init?.method ?? "GET",
        url: String(input),
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      });
      const next = answers.shift();
      if (!next) throw new Error("no answer queued for " + String(input));
      return next;
    },
  );
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

const json = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });

function renderPage(projects: Project[], over: { error?: "internal" } = {}) {
  const events: AppEvent[] = [];
  const onBack = vi.fn();
  const view = render(
    <ProjectsPage
      projects={projects}
      loaded
      error={over.error ?? null}
      onEvent={(e) => events.push(e)}
      nav={<span>nav</span>}
      onBack={onBack}
    />,
  );
  return { events, onBack, view };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ProjectsPage", () => {
  it("lists projects with their repositories and sandbox", () => {
    const { onBack } = renderPage([
      project(),
      project(
        { id: "p2", name: "lab" },
        { state: "failed", errorCode: "sandbox_network_missing" },
      ),
    ]);
    expect(screen.getByRole("heading", { name: en.title })).toBeInTheDocument();
    const apps = screen.getByRole("article", { name: "apps" });
    expect(within(apps).getByText("AP")).toHaveClass("pbadge");
    expect(within(apps).getByText(en.sandboxWarm)).toBeInTheDocument();
    expect(within(apps).getByText("2 repositories")).toBeInTheDocument();
    expect(within(apps).getByText("trick77/toolkit")).toBeInTheDocument();
    expect(within(apps).getByText("26.11.2026")).toBeInTheDocument();
    expect(within(apps).getByText("0.1.0-abc")).toBeInTheDocument();
    expect(
      within(apps).getByRole("button", { name: en.stop }),
    ).toBeInTheDocument();
    expect(within(apps).queryByRole("button", { name: en.start })).toBeNull();
    const lab = screen.getByRole("article", { name: "lab" });
    expect(within(lab).getByText(en.sandboxFailed)).toBeInTheDocument();
    expect(
      within(lab).getByText(errors.sandbox_network_missing),
    ).toBeInTheDocument();
    expect(within(lab).getByText(en.never)).toBeInTheDocument();
    expect(
      within(lab).getByRole("button", { name: en.start }),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: resources.en.shell.backToSession }),
    );
    expect(onBack).toHaveBeenCalled();
  });

  it("says a setup or stop left behind and offers start, rebuild and delete", () => {
    // Nothing runs (not busy): the operation did not finish.
    for (const [state, text] of [
      ["setup", en.staleSetup],
      ["stopping", en.staleStop],
    ] as const) {
      const { view } = renderPage([project({}, { state })]);
      const card = screen.getByRole("article", { name: "apps" });
      expect(within(card).getByRole("status")).toHaveTextContent(text);
      expect(within(card).getByRole("status")).toHaveClass("failed");
      expect(
        within(card)
          .getAllByRole("button")
          .map((b) => b.textContent),
      ).toEqual([en.start, en.rebuild, en.delete]);
      view.unmount();
    }
  });

  it("shows what runs while busy and offers nothing that would wait", () => {
    for (const [state, text] of [
      ["setup", en.busySetup],
      ["none", en.busySetup],
      ["stopping", en.busyStopping],
      ["deleting", en.busyDeleting],
      ["warm", en.busyWorking],
    ] as const) {
      const { view } = renderPage([project({}, { state, busy: true })]);
      const card = screen.getByRole("article", { name: "apps" });
      expect(within(card).getByRole("status")).toHaveTextContent(text);
      expect(within(card).getByRole("status")).toHaveClass("busy");
      expect(within(card).queryAllByRole("button")).toEqual([]);
      expect(within(card).getByText(en.busyNote)).toBeInTheDocument();
      view.unmount();
    }
  });

  it("drops an open confirmation once the sandbox turns busy", () => {
    const { view, events } = renderPage([project()]);
    fireEvent.click(screen.getByRole("button", { name: en.delete }));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    view.rerender(
      <ProjectsPage
        projects={[project({}, { state: "warm", busy: true })]}
        loaded
        error={null}
        onEvent={(e) => events.push(e)}
        nav={null}
      />,
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
    // Busy over: the confirmation does not come back on its own.
    view.rerender(
      <ProjectsPage
        projects={[project()]}
        loaded
        error={null}
        onEvent={(e) => events.push(e)}
        nav={null}
      />,
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.getByRole("button", { name: en.delete })).toBeVisible();
  });

  it("says sandbox_busy when an operation began meanwhile", async () => {
    serve([json(409, { code: "sandbox_busy" })]);
    renderPage([project({}, { state: "setup" })]);
    fireEvent.click(screen.getByRole("button", { name: en.delete }));
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: en.confirmDeleteAction }),
      );
    });
    expect(screen.getByRole("alert")).toHaveTextContent(errors.sandbox_busy);
  });

  it("offers the delete again for a project left deleting", () => {
    renderPage([project({}, { state: "deleting" })]);
    const card = screen.getByRole("article", { name: "apps" });
    expect(
      within(card)
        .getAllByRole("button")
        .map((b) => b.textContent),
    ).toEqual([en.delete]);
  });

  it("shows an empty list and a failed load", () => {
    const { view } = renderPage([]);
    expect(screen.getByText(en.empty)).toBeInTheDocument();
    view.unmount();
    renderPage([], { error: "internal" });
    expect(screen.getByRole("alert")).toHaveTextContent(errors.internal);
  });

  it("starts and stops a sandbox and leaves its state to the stream", async () => {
    const calls = serve([
      json(202, { state: "setup" }),
      json(202, { state: "stopped" }),
    ]);
    const { events, view } = renderPage([project({}, { state: "stopped" })]);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.start }));
    });
    view.rerender(
      <ProjectsPage
        projects={[project()]}
        loaded
        error={null}
        onEvent={(e) => events.push(e)}
        nav={null}
      />,
    );
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.stop }));
    });
    expect(calls.map((c) => c.url)).toEqual([
      "/api/projects/p1/sandbox/start",
      "/api/projects/p1/sandbox/stop",
    ]);
    // The answers' sandboxes may be older than what the stream has shown:
    // they are not applied.
    expect(events).toEqual([]);
    expect(
      screen.queryByRole("button", { name: resources.en.shell.backToSession }),
    ).toBeNull();
  });

  it("asks before a rebuild and says why one is refused", async () => {
    const calls = serve([json(409, { code: "project_has_sessions" })]);
    renderPage([project()]);
    fireEvent.click(screen.getByRole("button", { name: en.rebuild }));
    const ask = screen.getByRole("alertdialog");
    expect(ask).toHaveTextContent("Rebuild the sandbox of apps");
    fireEvent.click(within(ask).getByRole("button", { name: en.cancel }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(calls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: en.rebuild }));
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: en.confirmRebuildAction }),
      );
    });
    expect(calls[0]).toMatchObject({
      method: "POST",
      url: "/api/projects/p1/sandbox/rebuild",
    });
    expect(screen.getByRole("alert")).toHaveTextContent(
      errors.project_has_sessions,
    );
  });

  it("formats the repository count", () => {
    const repos = Array.from({ length: 1000 }, (_, i) => ({
      id: `r${i}`,
      fullName: `o/r${i}`,
      defaultBranch: "main",
    }));
    renderPage([project({ repos })]);
    expect(screen.getByText("1'000 repositories")).toBeInTheDocument();
  });

  it("names how many sessions are still open when a delete is refused", async () => {
    serve([json(409, { code: "project_has_sessions" })]);
    render(
      <ProjectsPage
        projects={[project()]}
        openSessions={{ p1: 1234, p2: 1 }}
        loaded
        error={null}
        onEvent={() => {}}
        nav={null}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: en.delete }));
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: en.confirmDeleteAction }),
      );
    });
    expect(screen.getByRole("alert")).toHaveTextContent(
      "1'234 sessions of this project are still open. Archive them first",
    );
  });

  it("deletes a project after confirmation", async () => {
    const calls = serve([json(204)]);
    const { events } = renderPage([project()]);
    fireEvent.click(screen.getByRole("button", { name: en.delete }));
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Delete apps?");
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: en.confirmDeleteAction }),
      );
    });
    expect(calls[0]).toMatchObject({
      method: "DELETE",
      url: "/api/projects/p1",
    });
    expect(events).toEqual([{ kind: "project.removed", projectId: "p1" }]);
  });

  it("creates a project and hands it on, the form closed", async () => {
    const created = project({ id: "p9", name: "cli" }, { state: "setup" });
    const calls = serve([
      json(200, {
        github: { state: "disconnected", available: false },
        claude: { signedIn: false },
        apiKeys: [],
      }),
      json(201, created),
    ]);
    const { events } = renderPage([]);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.add }));
    });
    const form = screen.getByRole("form", { name: en.createTitle });
    fireEvent.change(within(form).getByLabelText(en.byName), {
      target: { value: "cli/cli" },
    });
    fireEvent.click(within(form).getByRole("button", { name: en.byNameAdd }));
    await act(async () => {
      fireEvent.click(within(form).getByRole("button", { name: en.create }));
    });
    expect(calls[1]).toEqual({
      method: "POST",
      url: "/api/projects",
      body: { name: "cli", repos: ["cli/cli"] },
    });
    expect(events).toEqual([
      { kind: "project.updated", project: created, projectId: "p9" },
    ]);
    expect(screen.queryByRole("form")).toBeNull();
  });
});
