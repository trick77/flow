import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";

import type { AgentCatalog, ProviderInfo } from "../api/types.gen";
import {
  aboveCap,
  choiceKey,
  defaultAuth,
  defaultChoice,
  defaultEffort,
  defaultMode,
  defaultProject,
  efforts,
  findModel,
  loadCatalog,
  modelLabel,
  parseChoiceKey,
  refreshCatalog,
  resetCatalog,
  useCatalog,
  useKnownCatalog,
} from "./catalog";

const claude: ProviderInfo = {
  provider: "claude",
  available: true,
  claudeAuths: ["api_key"],
  models: [
    { id: "opus", label: "Opus" },
    { id: "haiku", label: "Haiku" },
  ],
  defaultModel: "haiku",
};
const catalog: AgentCatalog = { providers: [claude], modes: ["plan", "auto"] };

describe("agent catalog", () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    resetCatalog();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("is asked once, again when fresh, and again after a failure", async () => {
    fetchMock.mockImplementation(
      async () => new Response(JSON.stringify(catalog)),
    );
    await loadCatalog();
    await loadCatalog();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await loadCatalog(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    resetCatalog();
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "internal" }), { status: 500 }),
    );
    await expect(loadCatalog()).rejects.toThrow();
    await expect(loadCatalog()).resolves.toEqual(catalog);
  });

  it("names a model, or falls back to its id", () => {
    expect(modelLabel(catalog, "claude", "opus")).toBe("Opus");
    expect(modelLabel(catalog, "claude", "x")).toBe("x");
    expect(modelLabel(catalog, "opencode", "opus")).toBe("opus");
    expect(modelLabel(undefined, "claude", "opus")).toBe("opus");
  });

  it("picks the defaults", () => {
    expect(defaultChoice(catalog, {})?.model.id).toBe("haiku");
    expect(defaultChoice(catalog, { model: "opus" })?.model.id).toBe("opus");
    expect(
      defaultChoice(
        { ...catalog, providers: [{ ...claude, defaultModel: "gone" }] },
        {},
      )?.model.id,
    ).toBe("opus");
    expect(
      defaultChoice(
        { ...catalog, providers: [{ ...claude, available: false }] },
        {},
      ),
    ).toBeUndefined();

    // Plan, whatever was used before; without plan the most careful offered.
    expect(defaultMode(catalog)).toBe("plan");
    expect(defaultMode({ ...catalog, modes: ["ask", "plan", "auto"] })).toBe(
      "plan",
    );
    expect(defaultMode({ ...catalog, modes: ["ask", "auto"] })).toBe("ask");
    expect(defaultMode({ ...catalog, modes: [] })).toBeUndefined();

    expect(defaultAuth(claude)).toBe("api_key");
    expect(
      defaultAuth({
        ...claude,
        claudeAuths: ["api_key", "subscription_token"],
      }),
    ).toBe("subscription_token");
    expect(defaultAuth({ ...claude, claudeAuths: undefined })).toBeUndefined();

    const p = (id: string) => ({
      id,
      name: id,
      repos: [],
      sandbox: { state: "warm" as const },
      createdAt: "",
    });
    const list = [p("a"), p("b"), p("c")];
    // The session on screen first, the remembered one only without it.
    expect(defaultProject(list, { projectId: "c" }, "b")?.id).toBe("b");
    expect(defaultProject(list, { projectId: "c" })?.id).toBe("c");
    expect(defaultProject(list, { projectId: "c" }, "gone")?.id).toBe("c");
    expect(defaultProject(list, { projectId: "x" }, "b")?.id).toBe("b");
    expect(defaultProject(list, {})?.id).toBe("a");
    expect(defaultProject([], {})).toBeUndefined();
  });

  it("tells two providers' models with the same id apart", () => {
    const twin: AgentCatalog = {
      providers: [
        claude,
        {
          provider: "opencode",
          available: true,
          models: [{ id: "opus", label: "Opus via OpenRouter", via: "x" }],
        },
      ],
      modes: [],
    };
    expect(findModel(twin, "opus")?.provider.provider).toBe("claude");
    expect(findModel(twin, "opus", "opencode")?.model.label).toBe(
      "Opus via OpenRouter",
    );
    expect(findModel(twin, "haiku", "opencode")).toBeUndefined();
    expect(
      defaultChoice(twin, { provider: "opencode", model: "opus" })?.provider
        .provider,
    ).toBe("opencode");
    expect(choiceKey("opencode", "a:b")).toBe("opencode:a:b");
    expect(parseChoiceKey("opencode:a:b")).toEqual({
      provider: "opencode",
      model: "a:b",
    });
    expect(parseChoiceKey("nokey")).toBeUndefined();
  });

  it("offers the efforts with the agent's own first, and picks the default", () => {
    const leveled: ProviderInfo = {
      ...claude,
      efforts: ["low", "high", "max"],
      defaultEffort: "default",
    };
    expect(efforts(leveled)).toEqual(["default", "low", "high", "max"]);
    expect(efforts(claude)).toEqual([]);
    expect(efforts(undefined)).toEqual([]);
    // The remembered one while offered, else the catalog's default.
    expect(defaultEffort(leveled, {})).toBe("default");
    expect(defaultEffort(leveled, { effort: "max" })).toBe("max");
    expect(defaultEffort(leveled, { effort: "xhigh" })).toBe("default");
    expect(defaultEffort({ ...leveled, defaultEffort: "high" }, {})).toBe(
      "high",
    );
    expect(defaultEffort({ ...leveled, defaultEffort: "medium" }, {})).toBe(
      "default",
    );
    expect(defaultEffort(claude, { effort: "max" })).toBeUndefined();
  });

  it("compares modes with the cap in the catalog's order", () => {
    const modes = ["plan", "ask", "accept_edits", "auto"] as const;
    expect(aboveCap(modes, "auto", "ask")).toBe(true);
    expect(aboveCap(modes, "ask", "ask")).toBe(false);
    expect(aboveCap(modes, "plan", "ask")).toBe(false);
    expect(aboveCap(modes, "auto", undefined)).toBe(false);
    expect(aboveCap(modes, "full_access", "ask")).toBe(false);
  });
});

describe("shared catalog state", () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    resetCatalog();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  function Probe({ id }: { id: string }) {
    const [state, retry] = useCatalog();
    const known = useKnownCatalog();
    return (
      <div data-testid={id}>
        {state.state}
        {known ? ` known:${known.modes.join(",")}` : ""}
        <button type="button" onClick={retry}>
          retry {id}
        </button>
      </div>
    );
  }

  it("lets a load anywhere name the models everywhere", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "internal" }), { status: 500 }),
    );
    render(
      <>
        <Probe id="a" />
        <Probe id="b" />
      </>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("a")).toHaveTextContent("failed"),
    );
    expect(screen.getByTestId("b")).toHaveTextContent("failed");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(catalog)));
    fireEvent.click(screen.getByRole("button", { name: "retry b" }));
    await waitFor(() =>
      expect(screen.getByTestId("a")).toHaveTextContent(
        "ready known:plan,auto",
      ),
    );

    // A refresh that fails keeps the known catalog naming models.
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "internal" }), { status: 500 }),
    );
    act(() => refreshCatalog());
    await waitFor(() =>
      expect(screen.getByTestId("a")).toHaveTextContent(
        "failed known:plan,auto",
      ),
    );
  });
});
