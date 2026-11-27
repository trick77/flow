import { act, fireEvent, render, screen, within } from "@testing-library/react";

import type { ApiKey, Profile } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { SettingsPage } from "./SettingsPage";

const en = resources.en.settings;
const errors = resources.en.errors;

const profile: Profile = {
  gitName: "",
  gitEmail: "",
  language: "en",
  formatLocale: "en-CH",
  timeZone: "Europe/Zurich",
  sidebarGrouping: "status",
  configSync: { state: "never" },
};

const token: ApiKey = {
  id: "k1",
  kind: "claude_token",
  label: "",
  envName: "CLAUDE_CODE_OAUTH_TOKEN",
  createdAt: "2026-11-26T13:05:00Z",
};

interface Call {
  method: string;
  url: string;
  body?: unknown;
}

const json = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });

// serve answers the first load (profile, connections), then each call with the
// next queued response.
function serve(p: Profile, keys: ApiKey[], answers: Response[] = []) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      calls.push({
        method,
        url,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      });
      if (method === "GET" && url === "/api/profile") return json(200, p);
      if (method === "GET" && url === "/api/connections") {
        return json(200, {
          github: { state: "disconnected" },
          claude: { signedIn: false },
          apiKeys: keys,
        });
      }
      const next = answers.shift();
      if (!next) throw new Error("no answer queued for " + url);
      return next;
    }),
  );
  return calls;
}

async function renderPage() {
  const onBack = vi.fn();
  await act(async () => {
    render(<SettingsPage nav={null} onBack={onBack} />);
  });
  return onBack;
}

const row = (name: string) => screen.getByRole("group", { name });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SettingsPage", () => {
  it("shows the profile and which secrets are stored, never a value", async () => {
    serve({ ...profile, gitName: "Jan", gitEmail: "jan@example.com" }, [token]);
    await renderPage();
    expect(screen.getByLabelText(en.gitName)).toHaveValue("Jan");
    expect(screen.getByLabelText(en.gitEmail)).toHaveValue("jan@example.com");
    const claude = row(en.claudeToken);
    expect(within(claude).getByText(en.stored)).toBeInTheDocument();
    expect(
      within(claude).getByText("Stored 26.11.2026, 14:05"),
    ).toBeInTheDocument();
    expect(
      within(claude).getByRole("button", { name: en.replace }),
    ).toBeInTheDocument();
    const key = row(en.anthropicKey);
    expect(within(key).getByText(en.notStored)).toBeInTheDocument();
    expect(
      within(key).getByRole("button", { name: en.add }),
    ).toBeInTheDocument();
    expect(screen.queryByText(en.identityHalf)).toBeNull();
    expect(screen.getByRole("button", { name: en.save })).toBeDisabled();
  });

  it("says when the page cannot load", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json(503, { code: "sandbox_unavailable" })),
    );
    await renderPage();
    expect(screen.getByRole("alert")).toHaveTextContent(en.loadFailed);
    expect(screen.getByRole("alert")).toHaveTextContent(
      errors.sandbox_unavailable,
    );
  });

  it("saves the git identity and puts a refusal at its field", async () => {
    const calls = serve(
      profile,
      [],
      [
        json(400, { code: "invalid_request", field: "gitEmail" }),
        json(400, { code: "invalid_request", field: "gitName" }),
        json(500, { code: "internal" }),
        json(200, {
          ...profile,
          gitName: "Jan Muster",
          gitEmail: "jan@example.com",
        }),
        json(200, { ...profile, gitName: "Jan Muster" }),
      ],
    );
    await renderPage();
    fireEvent.change(screen.getByLabelText(en.gitName), {
      target: { value: "Jan Muster" },
    });
    fireEvent.change(screen.getByLabelText(en.gitEmail), {
      target: { value: "nope" },
    });
    const save = () =>
      act(async () => {
        fireEvent.click(screen.getByRole("button", { name: en.save }));
      });
    await save();
    expect(calls[calls.length - 1]).toEqual({
      method: "PATCH",
      url: "/api/profile",
      body: { gitName: "Jan Muster", gitEmail: "nope" },
    });
    expect(screen.getByRole("alert")).toHaveTextContent(en.invalidGitEmail);
    expect(screen.getByLabelText(en.gitEmail)).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await save();
    expect(screen.getByRole("alert")).toHaveTextContent(en.invalidGitName);
    await save();
    expect(screen.getByRole("alert")).toHaveTextContent(errors.internal);
    fireEvent.change(screen.getByLabelText(en.gitEmail), {
      target: { value: "jan@example.com" },
    });
    await save();
    expect(screen.getByText(en.saved)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("button", { name: en.save })).toBeDisabled();
    // Half an identity: sessions get none, and the page says so.
    fireEvent.change(screen.getByLabelText(en.gitEmail), {
      target: { value: "" },
    });
    expect(screen.queryByText(en.saved)).toBeNull();
    await save();
    expect(screen.getByText(en.identityHalf)).toBeInTheDocument();
  });

  it("stores a token from a blank password field and clears it after", async () => {
    const stored = { ...token, id: "k2", createdAt: "2026-11-27T09:00:00Z" };
    const calls = serve(
      profile,
      [],
      [
        json(400, { code: "invalid_request", field: "value" }),
        json(201, stored),
      ],
    );
    await renderPage();
    const claude = row(en.claudeToken);
    fireEvent.click(within(claude).getByRole("button", { name: en.add }));
    const field = within(claude).getByLabelText(en.claudeToken);
    expect(field).toHaveAttribute("type", "password");
    expect(field).toHaveAttribute("autocomplete", "off");
    expect(field).toHaveValue("");
    const store = within(claude).getByRole("button", { name: en.storeAction });
    expect(store).toBeDisabled();
    fireEvent.change(field, { target: { value: "sk-ant-oat01-FAKE" } });
    await act(async () => {
      fireEvent.click(store);
    });
    expect(calls[calls.length - 1]).toEqual({
      method: "POST",
      url: "/api/connections/api-keys",
      body: { kind: "claude_token", value: "sk-ant-oat01-FAKE" },
    });
    expect(within(claude).getByRole("alert")).toHaveTextContent(
      "at most 4'096 characters",
    );
    await act(async () => {
      fireEvent.click(
        within(claude).getByRole("button", { name: en.storeAction }),
      );
    });
    expect(within(claude).getByText(en.stored)).toBeInTheDocument();
    expect(
      within(claude).getByText("Stored 27.11.2026, 10:00"),
    ).toBeInTheDocument();
    expect(within(claude).queryByLabelText(en.claudeToken)).toBeNull();
    expect(document.body.textContent).not.toContain("FAKE");
    // Replace opens an empty field again.
    fireEvent.click(within(claude).getByRole("button", { name: en.replace }));
    expect(within(claude).getByLabelText(en.claudeToken)).toHaveValue("");
    fireEvent.click(within(claude).getByRole("button", { name: en.cancel }));
    expect(within(claude).queryByLabelText(en.claudeToken)).toBeNull();
  });

  it("deletes a stored key after confirmation", async () => {
    const key: ApiKey = {
      ...token,
      id: "k3",
      kind: "anthropic",
      envName: "ANTHROPIC_API_KEY",
    };
    const calls = serve(
      profile,
      [key],
      [json(404, { code: "not_found" }), json(204)],
    );
    await renderPage();
    const r = row(en.anthropicKey);
    fireEvent.click(within(r).getByRole("button", { name: en.remove }));
    expect(within(r).getByRole("alertdialog")).toHaveTextContent(
      "Delete the stored Anthropic API key?",
    );
    fireEvent.click(within(r).getByRole("button", { name: en.cancel }));
    expect(within(r).queryByRole("alertdialog")).toBeNull();
    fireEvent.click(within(r).getByRole("button", { name: en.remove }));
    await act(async () => {
      fireEvent.click(
        within(within(r).getByRole("alertdialog")).getByRole("button", {
          name: en.remove,
        }),
      );
    });
    expect(within(r).getByRole("alert")).toHaveTextContent(errors.not_found);
    fireEvent.click(within(r).getByRole("button", { name: en.remove }));
    await act(async () => {
      fireEvent.click(
        within(within(r).getByRole("alertdialog")).getByRole("button", {
          name: en.remove,
        }),
      );
    });
    expect(calls[calls.length - 1]).toMatchObject({
      method: "DELETE",
      url: "/api/connections/api-keys/k3",
    });
    expect(within(r).getByText(en.notStored)).toBeInTheDocument();
  });

  describe("GitHub", () => {
    // serveGitHub answers the connections with whatever github holds now.
    function serveGitHub(state: { github: unknown; fail?: boolean }) {
      const calls: string[] = [];
      vi.stubGlobal(
        "fetch",
        vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
          const url = String(input);
          calls.push(`${init?.method ?? "GET"} ${url}`);
          if (url === "/api/profile") return json(200, profile);
          if (url === "/api/connections") {
            if (state.fail && calls.length > 2) {
              return json(500, { code: "internal" });
            }
            return json(200, {
              github: state.github,
              claude: { signedIn: false },
              apiKeys: [],
            });
          }
          if (url === "/api/connections/github") return json(204);
          throw new Error("unexpected " + url);
        }),
      );
      return calls;
    }

    it("puts the GitHub row above the Claude rows", async () => {
      serveGitHub({ github: { state: "disconnected" } });
      await renderPage();
      const groups = screen
        .getAllByRole("group")
        .map((g) => g.getAttribute("aria-label"));
      expect(groups).toEqual([en.github, en.claudeToken, en.anthropicKey]);
    });

    it("reads the connections again on connectionsRev and after a disconnect", async () => {
      const state = {
        github: { state: "connected", login: "octo-user" } as unknown,
      };
      const calls = serveGitHub(state);
      const view = render(<SettingsPage nav={null} connectionsRev={0} />);
      await act(async () => undefined);
      const gh = row(en.github);
      expect(gh).toHaveTextContent("octo-user");
      state.github = { state: "expired", login: "octo-user" };
      await act(async () => {
        view.rerender(<SettingsPage nav={null} connectionsRev={1} />);
      });
      expect(gh).toHaveTextContent(en.githubExpiredHint);
      state.github = { state: "disconnected" };
      fireEvent.click(within(gh).getByRole("button", { name: en.disconnect }));
      await act(async () => {
        fireEvent.click(
          within(within(gh).getByRole("alertdialog")).getByRole("button", {
            name: en.disconnect,
          }),
        );
      });
      expect(gh).toHaveTextContent(en.pillNotConnected);
      expect(calls.filter((c) => c === "GET /api/connections")).toHaveLength(3);
    });

    it("keeps what it shows when a later read fails", async () => {
      serveGitHub({
        github: { state: "connected", login: "octo-user" },
        fail: true,
      });
      const view = render(<SettingsPage nav={null} connectionsRev={0} />);
      await act(async () => undefined);
      await act(async () => {
        view.rerender(<SettingsPage nav={null} connectionsRev={1} />);
      });
      expect(row(en.github)).toHaveTextContent("octo-user");
    });

    it("shows a confirmed connection at once", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: false });
      const state = { github: { state: "disconnected" } as unknown };
      const calls: string[] = [];
      vi.stubGlobal(
        "fetch",
        vi.fn(async (input: RequestInfo | URL) => {
          const url = String(input);
          calls.push(url);
          if (url === "/api/profile") return json(200, profile);
          if (url === "/api/connections") {
            return json(200, {
              github: state.github,
              claude: { signedIn: false },
              apiKeys: [],
            });
          }
          if (url === "/api/connections/github/device") {
            return json(200, {
              userCode: "ABCD-1234",
              verificationUri: "https://github.com/login/device",
              expiresAt: new Date(Date.now() + 900_000).toISOString(),
              intervalSeconds: 5,
            });
          }
          state.github = { state: "connected", login: "octo-user" };
          return json(200, { state: "connected", login: "octo-user" });
        }),
      );
      try {
        await renderPage();
        const gh = row(en.github);
        await act(async () => {
          fireEvent.click(within(gh).getByRole("button", { name: en.connect }));
        });
        await act(() => vi.advanceTimersByTimeAsync(5000));
        expect(within(gh).getAllByRole("status")[0]).toHaveTextContent(
          en.pillConnected,
        );
        expect(gh).toHaveTextContent("Connected as octo-user.");
        expect(calls.filter((c) => c === "/api/connections")).toHaveLength(2);
      } finally {
        vi.useRealTimers();
      }
    });
  });
});
