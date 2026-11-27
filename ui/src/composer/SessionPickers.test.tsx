import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { AgentCatalog } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { SessionPickers } from "./SessionPickers";

const en = resources.en;

const catalog: AgentCatalog = {
  providers: [
    {
      provider: "claude",
      available: true,
      models: [
        { id: "claude-opus-5-5", label: "Opus 5.5" },
        { id: "claude-haiku-5-5", label: "Haiku 5.5" },
      ],
    },
    {
      provider: "opencode",
      available: true,
      models: [{ id: "qwen", label: "Qwen3 Coder", via: "openrouter" }],
    },
  ],
  modes: ["plan", "ask", "accept_edits", "auto", "full_access"],
};

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status });
}

describe("SessionPickers", () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  function view(over: Partial<Parameters<typeof SessionPickers>[0]> = {}) {
    const props = { onUpdated: vi.fn(), onError: vi.fn() };
    render(
      <SessionPickers
        sessionId="s1"
        provider="claude"
        model="claude-opus-5-5"
        mode="ask"
        catalog={catalog}
        turnRunning={false}
        {...props}
        {...over}
      />,
    );
    return props;
  }

  it("changes the model at once and keeps the answer", async () => {
    let answer: (r: Response) => void = () => {};
    fetchMock.mockReturnValueOnce(new Promise((r) => (answer = r)));
    const props = view();
    fireEvent.click(
      screen.getByRole("button", { name: "Model: Claude Code, Opus 5.5" }),
    );
    // Only the session's own agent's models.
    expect(screen.queryByRole("menuitemradio", { name: /Qwen/ })).toBeNull();
    fireEvent.click(screen.getByRole("menuitemradio", { name: /Haiku/ }));
    const picker = screen.getByRole("button", {
      name: "Model: Claude Code, Haiku 5.5",
    });
    expect(picker).toBeDisabled();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/s1");
    expect(fetchMock.mock.calls[0][1]?.method).toBe("PATCH");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      model: "claude-haiku-5-5",
    });
    answer(
      json(200, {
        summary: { model: "claude-haiku-5-5", mode: "ask" },
      }),
    );
    await waitFor(() =>
      expect(props.onUpdated).toHaveBeenCalledWith({
        model: "claude-haiku-5-5",
      }),
    );
    expect(props.onError).toHaveBeenLastCalledWith(null);
  });

  it("puts the old mode back on a refusal", async () => {
    fetchMock.mockResolvedValueOnce(json(501, { code: "not_implemented" }));
    const props = view();
    fireEvent.click(
      screen.getByRole("button", { name: "Permission mode: Ask" }),
    );
    fireEvent.click(screen.getByRole("menuitemradio", { name: /^Auto/ }));
    expect(
      screen.getByRole("button", { name: "Permission mode: Auto" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(props.onError).toHaveBeenLastCalledWith("not_implemented"),
    );
    expect(
      screen.getByRole("button", { name: "Permission mode: Ask" }),
    ).toBeEnabled();
    expect(props.onUpdated).not.toHaveBeenCalled();
  });

  it("disables the modes above the cap and says why", () => {
    view({ modeCap: "accept_edits" });
    fireEvent.click(
      screen.getByRole("button", { name: "Permission mode: Ask" }),
    );
    expect(screen.getByText(en.composer.modeCapped)).toBeInTheDocument();
    expect(
      screen.getByRole("menuitemradio", { name: /Accept edits/ }),
    ).toBeEnabled();
    expect(screen.getByRole("menuitemradio", { name: /^Auto/ })).toBeDisabled();
    expect(
      screen.getByRole("menuitemradio", { name: /Full access/ }),
    ).toBeDisabled();
  });

  it("takes a model while a turn runs, for the next turn, and says so", () => {
    fetchMock.mockReturnValueOnce(new Promise(() => {}));
    view({ turnRunning: true });
    const model = screen.getByRole("button", { name: /^Model:/ });
    expect(model).toBeEnabled();
    expect(model).toHaveAttribute("title", en.composer.modelNextTurn);
    expect(
      screen.getByRole("button", { name: /^Permission mode/ }),
    ).toBeEnabled();
    fireEvent.click(model);
    expect(screen.getByRole("menu")).toHaveTextContent(
      en.composer.modelNextTurn,
    );
    fireEvent.click(screen.getByRole("menuitemradio", { name: /Haiku/ }));
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      model: "claude-haiku-5-5",
    });
  });

  it("gives no next-turn hint between turns", () => {
    view();
    const model = screen.getByRole("button", { name: /^Model:/ });
    expect(model).not.toHaveAttribute("title");
    fireEvent.click(model);
    expect(screen.getByRole("menu")).not.toHaveTextContent(
      en.composer.modelNextTurn,
    );
  });

  it("sends nothing for the value already set", () => {
    view();
    fireEvent.click(screen.getByRole("button", { name: /^Permission mode/ }));
    fireEvent.click(screen.getByRole("menuitemradio", { name: /^Ask/ }));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("is read-only for an agent the catalog does not list", () => {
    view({
      provider: "opencode",
      model: "glm",
      catalog: { ...catalog, providers: [] },
    });
    expect(
      screen.getByRole("img", { name: "Model: OpenCode, glm" }),
    ).toBeInTheDocument();
  });
});
