import type { AgentCatalog, ProviderInfo } from "../api/types.gen";
import {
  defaultAuth,
  defaultChoice,
  defaultMode,
  defaultProject,
  loadCatalog,
  modelLabel,
  resetCatalog,
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

    expect(defaultMode(catalog, {})).toBe("plan");
    expect(defaultMode(catalog, { mode: "auto" })).toBe("auto");
    expect(defaultMode({ ...catalog, modes: ["plan", "ask"] }, {})).toBe("ask");

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
    expect(defaultProject(list, { projectId: "c" }, "b")?.id).toBe("c");
    expect(defaultProject(list, { projectId: "x" }, "b")?.id).toBe("b");
    expect(defaultProject(list, {})?.id).toBe("a");
    expect(defaultProject([], {})).toBeUndefined();
  });
});
