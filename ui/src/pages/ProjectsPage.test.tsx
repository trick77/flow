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

  it("offers start, rebuild and delete in setup and stopping, never a stop", () => {
    // Such a state can outlive its operation (a restart mid bring-up); while
    // one really runs the server answers sandbox_busy.
    for (const state of ["setup", "stopping"] as const) {
      const { view } = renderPage([project({}, { state })]);
      const card = screen.getByRole("article", { name: "apps" });
      expect(
        within(card)
          .getAllByRole("button")
          .map((b) => b.textContent),
      ).toEqual([en.start, en.rebuild, en.delete]);
      view.unmount();
    }
  });

  it("says sandbox_busy when a setup really runs", async () => {
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

  it("creates a project from a name and repositories, one per line", async () => {
    const created = project({ id: "p9", name: "new" }, { state: "setup" });
    const calls = serve([json(201, created)]);
    const { events } = renderPage([]);
    fireEvent.click(screen.getByRole("button", { name: en.add }));
    const form = screen.getByRole("form", { name: en.createTitle });
    const submit = within(form).getByRole("button", { name: en.create });
    expect(submit).toBeDisabled();
    fireEvent.change(within(form).getByLabelText(en.name), {
      target: { value: " new " },
    });
    fireEvent.change(within(form).getByLabelText(en.repos), {
      target: {
        value: "trick77/toolkit\n\n https://github.com/trick77/ship \n",
      },
    });
    await act(async () => {
      fireEvent.click(submit);
    });
    expect(calls[0]).toEqual({
      method: "POST",
      url: "/api/projects",
      body: {
        name: "new",
        repos: ["trick77/toolkit", "https://github.com/trick77/ship"],
      },
    });
    expect(events).toEqual([
      { kind: "project.updated", project: created, projectId: "p9" },
    ]);
    expect(screen.queryByRole("form")).toBeNull();
  });

  it("puts a refusal of the create form at its field", async () => {
    serve([
      json(409, { code: "conflict", field: "name" }),
      json(400, { code: "invalid_request", field: "name" }),
      json(400, { code: "invalid_request", field: "repos" }),
      json(502, { code: "upstream" }),
    ]);
    renderPage([]);
    fireEvent.click(screen.getByRole("button", { name: en.add }));
    fireEvent.change(screen.getByLabelText(en.name), {
      target: { value: "apps" },
    });
    fireEvent.change(screen.getByLabelText(en.repos), {
      target: { value: "a/b" },
    });
    const submit = () =>
      act(async () => {
        fireEvent.click(screen.getByRole("button", { name: en.create }));
      });
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(en.nameTaken);
    expect(screen.getByLabelText(en.name)).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(en.invalidName);
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(en.invalidRepos);
    expect(screen.getByLabelText(en.repos)).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
    fireEvent.click(screen.getByRole("button", { name: en.close }));
    expect(screen.queryByRole("form")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: en.add }));
    fireEvent.click(screen.getByRole("button", { name: en.cancel }));
    expect(screen.queryByRole("form")).toBeNull();
  });

  it("says why a repository could not be looked up, not that it is malformed", async () => {
    serve([
      json(409, { code: "github_not_connected", field: "repos" }),
      json(502, { code: "upstream", field: "repos" }),
    ]);
    renderPage([]);
    fireEvent.click(screen.getByRole("button", { name: en.add }));
    fireEvent.change(screen.getByLabelText(en.name), {
      target: { value: "apps" },
    });
    fireEvent.change(screen.getByLabelText(en.repos), {
      target: { value: "octo/hidden" },
    });
    const submit = () =>
      act(async () => {
        fireEvent.click(screen.getByRole("button", { name: en.create }));
      });
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(
      errors.github_not_connected,
    );
    expect(screen.getByRole("alert")).not.toHaveTextContent(en.invalidRepos);
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
  });
});
