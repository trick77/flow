import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";

import type { AgentCatalog, Project } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { resetCatalog } from "./catalog";
import { NewSession } from "./NewSession";
import { PREFS_KEY } from "./prefs";

const en = resources.en;

const projects: Project[] = [
  {
    id: "p1",
    name: "riptide",
    repos: [
      { id: "r1", fullName: "dev/riptide", defaultBranch: "master" },
      { id: "r2", fullName: "dev/riptide-docs", defaultBranch: "main" },
    ],
    sandbox: { state: "warm" },
    createdAt: "2026-11-01T00:00:00Z",
  },
  {
    id: "p2",
    name: "harbor",
    repos: [{ id: "r3", fullName: "dev/harbor", defaultBranch: "master" }],
    sandbox: { state: "stopped" },
    createdAt: "2026-11-01T00:00:00Z",
  },
];

const catalog: AgentCatalog = {
  providers: [
    {
      provider: "claude",
      available: true,
      claudeAuths: ["subscription_token"],
      models: [
        { id: "claude-opus-5-5", label: "Opus 5.5" },
        { id: "claude-haiku-5-5", label: "Haiku 5.5" },
      ],
      defaultModel: "claude-opus-5-5",
    },
    {
      provider: "opencode",
      available: false,
      models: [{ id: "qwen", label: "Qwen3 Coder", via: "openrouter" }],
    },
  ],
  modes: ["plan", "ask", "accept_edits", "auto", "full_access"],
};

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status });
}

describe("NewSession", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    resetCatalog();
    window.localStorage.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(crypto, "randomUUID").mockReturnValue(
      "0193a8f2-0000-7000-8000-000000000009",
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  function serve(cat: AgentCatalog | { code: string } = catalog, status = 200) {
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/api/agents") return json(status, cat);
      if (url === "/api/sessions" && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        return json(201, {
          summary: { id: "s-new", projectId: body.projectId },
          worktrees: [],
          dir: "/w",
          lastSeq: 0,
        });
      }
      return json(404, { code: "not_found" });
    });
  }

  function view(over: Partial<Parameters<typeof NewSession>[0]> = {}) {
    const props = {
      onCreated: vi.fn(),
      onOpenSettings: vi.fn(),
      onOpenProjects: vi.fn(),
    };
    render(<NewSession projects={projects} {...props} {...over} />);
    return props;
  }

  type Props = Parameters<typeof NewSession>[0];
  function viewWithRerender(over: Partial<Props> = {}) {
    const props: Props = {
      projects,
      onCreated: vi.fn(),
      onOpenSettings: vi.fn(),
      onOpenProjects: vi.fn(),
      ...over,
    };
    const r = render(<NewSession {...props} />);
    return {
      props,
      rerender: (p: Props) => r.rerender(<NewSession {...p} />),
    };
  }

  const box = () => screen.findByLabelText(en.composer.startPlaceholder);
  const startButton = () =>
    screen.getByRole("button", { name: en.composer.start });
  const posted = () => {
    const call = fetchMock.mock.calls.find(
      ([url, init]) => url === "/api/sessions" && init?.method === "POST",
    );
    return call ? JSON.parse(String(call[1]?.body)) : undefined;
  };

  it("starts a session with every repository, the default model and Ask", async () => {
    serve();
    const props = view();
    fireEvent.change(await box(), { target: { value: "  Fix the test  " } });
    expect(
      screen.getByRole("heading", { level: 2, name: en.composer.newTitle }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("What should we work on in riptide?"),
    ).toBeInTheDocument();
    const repos = within(
      screen.getByRole("group", { name: en.composer.repos }),
    ).getAllByRole("checkbox");
    expect(repos).toHaveLength(2);
    repos.forEach((r) => expect(r).toBeChecked());
    expect(
      screen.getByRole("button", { name: "Model: Claude Code, Opus 5.5" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Permission mode: Plan" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Runs in the riptide sandbox")).toBeInTheDocument();

    fireEvent.keyDown(await box(), { key: "Enter" });
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
    expect(posted()).toEqual({
      projectId: "p1",
      repoIds: ["r1", "r2"],
      provider: "claude",
      claudeAuth: "subscription_token",
      model: "claude-opus-5-5",
      mode: "plan",
      message: {
        clientId: "0193a8f2-0000-7000-8000-000000000009",
        text: "Fix the test",
      },
    });
    expect(props.onCreated.mock.calls[0][0].summary.id).toBe("s-new");
    // The mode is not remembered.
    expect(JSON.parse(window.localStorage.getItem(PREFS_KEY)!)).toEqual({
      projectId: "p1",
      provider: "claude",
      model: "claude-opus-5-5",
    });
  });

  it("takes the picked project, repositories, model and mode", async () => {
    serve();
    const props = view({ currentProjectId: "p2" });
    await box();
    // The session on screen gives the project when nothing is remembered.
    expect(
      screen.getByText("What should we work on in harbor?"),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Project: harbor" }));
    const projectMenu = screen.getByRole("menu", { name: "Project: harbor" });
    expect(
      within(projectMenu).getByRole("menuitemradio", { name: /harbor/ }),
    ).toHaveFocus();
    fireEvent.click(
      within(projectMenu).getByRole("menuitemradio", { name: /riptide/ }),
    );
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Project: riptide" }),
    ).toHaveFocus();

    fireEvent.click(screen.getByRole("checkbox", { name: "dev/riptide-docs" }));
    fireEvent.click(screen.getByRole("button", { name: /^Model:/ }));
    const modelMenu = screen.getByRole("menu");
    expect(within(modelMenu).getByText("OpenCode")).toBeInTheDocument();
    expect(
      within(modelMenu).getByRole("menuitemradio", { name: /Qwen3 Coder/ }),
    ).toBeDisabled();
    fireEvent.click(
      within(modelMenu).getByRole("menuitemradio", { name: /Haiku 5.5/ }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^Permission mode:/ }));
    const full = screen.getByRole("menuitemradio", { name: /Full access/ });
    expect(full).toHaveClass("warn");
    fireEvent.click(full);

    fireEvent.change(await box(), { target: { value: "Go" } });
    fireEvent.click(startButton());
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
    expect(posted()).toMatchObject({
      projectId: "p1",
      repoIds: ["r1"],
      model: "claude-haiku-5-5",
      mode: "full_access",
    });

    // The next new session keeps the project and model, and starts in plan.
    cleanup();
    view();
    await box();
    expect(
      screen.getByRole("button", { name: "Model: Claude Code, Haiku 5.5" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Permission mode: Plan" }),
    ).toBeInTheDocument();
  });

  it("starts from what this browser used last, while it is still valid", async () => {
    window.localStorage.setItem(
      PREFS_KEY,
      JSON.stringify({
        projectId: "p2",
        model: "claude-haiku-5-5",
        mode: "full_access",
      }),
    );
    serve();
    view();
    await box();
    // Without a session on screen the remembered project applies.
    expect(
      screen.getByRole("button", { name: "Project: harbor" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Model: Claude Code, Haiku 5.5" }),
    ).toBeInTheDocument();
    // A mode an earlier version remembered does not apply: plan, always.
    expect(
      screen.getByRole("button", { name: "Permission mode: Plan" }),
    ).toBeInTheDocument();
  });

  it("takes the project of the session on screen over the remembered one", async () => {
    window.localStorage.setItem(PREFS_KEY, JSON.stringify({ projectId: "p2" }));
    serve();
    const { rerender, props } = viewWithRerender({ currentProjectId: "p1" });
    await box();
    expect(
      screen.getByRole("button", { name: "Project: riptide" }),
    ).toBeInTheDocument();
    // An empty draft follows the session on screen.
    rerender({ ...props, currentProjectId: "p2" });
    expect(
      screen.getByRole("button", { name: "Project: harbor" }),
    ).toBeInTheDocument();
    // A draft keeps its project whatever is opened meanwhile.
    fireEvent.change(await box(), { target: { value: "Go" } });
    rerender({ ...props, currentProjectId: "p1" });
    expect(
      screen.getByRole("button", { name: "Project: harbor" }),
    ).toBeInTheDocument();
  });

  it("keys the model by its agent: the same id from two agents", async () => {
    window.localStorage.setItem(
      PREFS_KEY,
      JSON.stringify({ provider: "opencode", model: "claude-opus-5-5" }),
    );
    const twin: AgentCatalog = {
      ...catalog,
      providers: [
        catalog.providers[0],
        {
          provider: "opencode",
          available: true,
          models: [
            { id: "claude-opus-5-5", label: "Opus 5.5", via: "openrouter" },
          ],
        },
      ],
    };
    serve(twin);
    const props = view();
    await box();
    expect(
      screen.getByRole("button", { name: "Model: OpenCode, Opus 5.5" }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Model:/ }));
    const opts = within(screen.getByRole("menu")).getAllByRole(
      "menuitemradio",
      { name: /Opus 5.5/ },
    );
    expect(opts).toHaveLength(2);
    // Only the OpenCode row is checked, not every row with the id.
    expect(
      opts.filter((o) => o.getAttribute("aria-checked") === "true"),
    ).toEqual([opts[1]]);
    fireEvent.click(opts[0]);
    fireEvent.change(await box(), { target: { value: "Go" } });
    fireEvent.click(startButton());
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
    expect(posted()).toMatchObject({
      provider: "claude",
      model: "claude-opus-5-5",
    });
  });

  it("says when the projects are loading or failed, not to create one", async () => {
    serve();
    const { rerender, props } = viewWithRerender({
      projects: [],
      projectsLoaded: false,
    });
    expect(screen.getByRole("status")).toHaveTextContent(
      en.composer.projectsLoading,
    );
    rerender({
      ...props,
      projects: [],
      projectsLoaded: true,
      projectsError: "internal",
    });
    expect(screen.getByRole("alert")).toHaveTextContent(
      en.composer.projectsFailed,
    );
    expect(screen.queryByText(en.composer.noProject)).toBeNull();
  });

  it("keeps the client id across retries and reloads the list after a lost answer", async () => {
    let calls = 0;
    const ids: string[] = [];
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/api/agents") return json(200, catalog);
      if (url === "/api/sessions" && init?.method === "POST") {
        ids.push(JSON.parse(String(init.body)).message.clientId);
        calls++;
        if (calls === 1) throw new TypeError("network");
        return json(201, {
          summary: { id: "s-new", projectId: "p1" },
          worktrees: [],
          dir: "/w",
          lastSeq: 0,
        });
      }
      return json(404, { code: "not_found" });
    });
    const uuids = vi.spyOn(crypto, "randomUUID");
    uuids.mockReturnValueOnce("0193a8f2-0000-7000-8000-00000000000a");
    uuids.mockReturnValueOnce("0193a8f2-0000-7000-8000-00000000000b");
    let settle!: () => void;
    const onAnswerLost = vi.fn(
      () => new Promise<void>((resolve) => (settle = resolve)),
    );
    const props = view({ onAnswerLost });
    fireEvent.change(await box(), { target: { value: "Go" } });
    fireEvent.click(startButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.composer.startLostChecking,
    );
    expect(onAnswerLost).toHaveBeenCalledTimes(1);
    // No retry while the list is being reloaded.
    expect(startButton()).toBeDisabled();
    act(() => settle());
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        en.composer.startLost,
      ),
    );
    expect(startButton()).toBeEnabled();
    fireEvent.click(startButton());
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
    expect(ids).toEqual([
      "0193a8f2-0000-7000-8000-00000000000a",
      "0193a8f2-0000-7000-8000-00000000000a",
    ]);
  });

  it("does not reload the list after a plain refusal", async () => {
    fetchMock.mockImplementation(async (input) =>
      String(input) === "/api/agents"
        ? json(200, catalog)
        : json(501, { code: "not_implemented" }),
    );
    const onAnswerLost = vi.fn(() => Promise.resolve());
    view({ onAnswerLost });
    fireEvent.change(await box(), { target: { value: "Go" } });
    fireEvent.click(startButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.not_implemented,
    );
    expect(onAnswerLost).not.toHaveBeenCalled();
  });

  it("asks for the catalog again when it shows again", async () => {
    serve();
    const { rerender, props } = viewWithRerender();
    await box();
    const agents = () =>
      fetchMock.mock.calls.filter(([u]) => u === "/api/agents").length;
    expect(agents()).toBe(1);
    rerender({ ...props, hidden: true });
    expect(screen.getByRole("region", { hidden: true })).not.toBeVisible();
    expect(agents()).toBe(1);
    rerender({ ...props, hidden: false });
    await waitFor(() => expect(agents()).toBe(2));
  });

  it("ignores remembered values the lists no longer hold", async () => {
    window.localStorage.setItem(
      PREFS_KEY,
      JSON.stringify({ projectId: "gone", model: "qwen", mode: "yolo" }),
    );
    serve();
    view();
    await box();
    expect(
      screen.getByRole("button", { name: "Project: riptide" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Model: Claude Code, Opus 5.5" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Permission mode: Plan" }),
    ).toBeInTheDocument();
  });

  it("needs a repository and a message", async () => {
    serve();
    view();
    fireEvent.change(await box(), { target: { value: "Go" } });
    expect(startButton()).toBeEnabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "dev/riptide" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "dev/riptide-docs" }));
    expect(screen.getByRole("alert")).toHaveTextContent(en.composer.reposNone);
    expect(startButton()).toBeDisabled();
    fireEvent.keyDown(await box(), { key: "Enter" });
    fireEvent.click(screen.getByRole("checkbox", { name: "dev/riptide" }));
    fireEvent.change(await box(), { target: { value: "   " } });
    expect(startButton()).toBeDisabled();
    expect(posted()).toBeUndefined();
  });

  it("shows a refusal at the form and keeps the draft", async () => {
    serve();
    fetchMock.mockImplementation(async (input) =>
      String(input) === "/api/agents"
        ? json(200, catalog)
        : json(409, { code: "sandbox_memory_full" }),
    );
    const props = view();
    fireEvent.change(await box(), { target: { value: "Go" } });
    fireEvent.click(startButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.sandbox_memory_full,
    );
    expect(await box()).toHaveValue("Go");
    expect(props.onCreated).not.toHaveBeenCalled();
    expect(startButton()).toBeEnabled();
  });

  it("offers the credential when more than one can be used", async () => {
    serve({
      ...catalog,
      providers: [
        {
          ...catalog.providers[0],
          claudeAuths: ["api_key", "subscription_token"],
        },
      ],
    });
    view();
    await box();
    fireEvent.click(
      screen.getByRole("button", {
        name: "Claude credential: Subscription token",
      }),
    );
    fireEvent.click(screen.getByRole("menuitemradio", { name: /API key/ }));
    fireEvent.change(await box(), { target: { value: "Go" } });
    fireEvent.click(startButton());
    await waitFor(() => expect(posted()?.claudeAuth).toBe("api_key"));
  });

  it("points to Settings when no agent can run", async () => {
    serve({
      ...catalog,
      providers: [
        { ...catalog.providers[0], available: false, claudeAuths: [] },
      ],
    });
    const props = view();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.composer.noAgent,
    );
    expect(screen.queryByLabelText(en.composer.startPlaceholder)).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: en.composer.openSettings }),
    );
    expect(props.onOpenSettings).toHaveBeenCalled();
  });

  it("points to Projects without a project", async () => {
    serve();
    const props = view({ projects: [] });
    expect(screen.getByRole("status")).toHaveTextContent(en.composer.noProject);
    fireEvent.click(
      screen.getByRole("button", { name: en.composer.openProjects }),
    );
    expect(props.onOpenProjects).toHaveBeenCalled();
  });

  it("says when the catalog fails and asks again", async () => {
    serve({ code: "internal" }, 500);
    view();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.composer.catalogFailed,
    );
    serve();
    fireEvent.click(screen.getByRole("button", { name: en.composer.retry }));
    expect(await box()).toBeInTheDocument();
  });
});
