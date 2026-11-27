import { act, fireEvent, render, screen } from "@testing-library/react";

import { App } from "../App";
import { resources } from "../i18n/resources";
import { FakeEventSource } from "../test/fakeEventSource";
import { AuthGate } from "./AuthGate";

const en = resources.en;

type Answer = { status?: number; body?: unknown };

// serve answers by "METHOD path"; anything else is not_found.
function serve(routes: Record<string, Answer | (() => Answer)>) {
  const calls: string[] = [];
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${String(input)}`;
      calls.push(key);
      const route = routes[key];
      const a = typeof route === "function" ? route() : route;
      if (!a) {
        return new Response(JSON.stringify({ code: "not_found" }), {
          status: 404,
        });
      }
      const status = a.status ?? 200;
      return new Response(
        status === 204 ? null : JSON.stringify(a.body ?? {}),
        { status },
      );
    },
  );
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

const signedIn = {
  body: { signedIn: true, mode: "oidc", name: "Owner Name" },
};
const lists = {
  "GET /api/sessions": { body: { items: [] } },
  "GET /api/projects": { body: { items: [] } },
};

function renderApp() {
  render(
    <AuthGate>
      <App EventSource={FakeEventSource} />
    </AuthGate>,
  );
}

async function expectSignIn() {
  const link = await screen.findByRole("link", { name: en.shell.signIn });
  expect(link).toHaveAttribute("href", "/api/auth/login");
  expect(
    screen.getByRole("heading", { level: 1, name: en.common.appName }),
  ).toBeInTheDocument();
  expect(screen.getByText(en.shell.signInHint)).toBeInTheDocument();
  // The app is not rendered behind it.
  expect(screen.queryByRole("navigation")).toBeNull();
}

describe("AuthGate", () => {
  beforeEach(() => {
    FakeEventSource.reset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the sign-in screen when signed out", async () => {
    const calls = serve({
      "GET /api/auth/session": { body: { signedIn: false, mode: "oidc" } },
    });
    renderApp();
    await expectSignIn();
    expect(screen.queryByRole("alert")).toBeNull();
    // Nothing behind the gate asked for data.
    expect(calls).toEqual(["GET /api/auth/session"]);
  });

  it("shows the sign-in screen with the error when the check fails", async () => {
    serve({
      "GET /api/auth/session": { status: 500, body: { code: "internal" } },
    });
    renderApp();
    await expectSignIn();
    expect(screen.getByRole("alert")).toHaveTextContent(en.errors.internal);
  });

  it("swaps to sign-in when an API call answers unauthenticated", async () => {
    let expired = false;
    serve({
      "GET /api/auth/session": signedIn,
      "GET /api/sessions": () =>
        expired
          ? { status: 401, body: { code: "unauthenticated" } }
          : { body: { items: [] } },
      "GET /api/projects": { body: { items: [] } },
    });
    renderApp();
    expect(await screen.findByText(en.shell.empty)).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "Account: Owner Name",
      }),
    ).toHaveTextContent("Owner Name");

    // The session runs out; the next reload of the lists answers 401.
    expired = true;
    act(() => FakeEventSource.forUrl("/api/events")[0].open());
    await expectSignIn();
  });

  it("swaps to sign-in when a stream ends and the session is gone", async () => {
    let session: Answer = signedIn;
    serve({ "GET /api/auth/session": () => session, ...lists });
    renderApp();
    expect(await screen.findByText(en.shell.empty)).toBeInTheDocument();
    const events = FakeEventSource.forUrl("/api/events")[0];

    // Gave up while the session still holds, or the check failing: stays.
    await act(async () => events.fail(true));
    session = { status: 500, body: { code: "internal" } };
    await act(async () => events.fail(true));
    expect(screen.getByText(en.shell.empty)).toBeInTheDocument();

    // The session expired: the stream's 401 is only "closed", the check says.
    session = { body: { signedIn: false, mode: "oidc" } };
    await act(async () => events.fail(true));
    await expectSignIn();
  });

  it("says once that the last sign-in was refused", async () => {
    window.history.pushState(null, "", "/?signin=failed");
    serve({
      "GET /api/auth/session": { body: { signedIn: false, mode: "oidc" } },
    });
    renderApp();
    await expectSignIn();
    expect(screen.getByRole("alert")).toHaveTextContent(en.shell.signInFailed);
    expect(window.location.search).toBe("");
  });

  it("signs out from the user menu", async () => {
    const calls = serve({
      "GET /api/auth/session": signedIn,
      "POST /api/auth/logout": { status: 204 },
      ...lists,
    });
    renderApp();
    const account = await screen.findByRole("button", {
      name: "Account: Owner Name",
    });
    expect(account).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu")).toBeNull();

    // Escape and a click elsewhere close the menu.
    fireEvent.click(account);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Enter" });
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    fireEvent.click(account);
    fireEvent.mouseDown(screen.getByRole("menuitem"));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("menu")).toBeNull();

    fireEvent.click(account);
    expect(account).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(screen.getByRole("menuitem", { name: en.shell.signOut }));
    await expectSignIn();
    expect(calls).toContain("POST /api/auth/logout");
  });

  it("lets a session without a name in", async () => {
    serve({
      "GET /api/auth/session": { body: { signedIn: true, mode: "dev" } },
      ...lists,
    });
    renderApp();
    expect(await screen.findByText(en.shell.empty)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Account:" }),
    ).toBeInTheDocument();
  });

  it("stays signed in when signing out fails", async () => {
    serve({
      "GET /api/auth/session": signedIn,
      "POST /api/auth/logout": { status: 500, body: { code: "internal" } },
      ...lists,
    });
    renderApp();
    fireEvent.click(
      await screen.findByRole("button", { name: "Account: Owner Name" }),
    );
    fireEvent.click(screen.getByRole("menuitem", { name: en.shell.signOut }));
    await act(async () => {});
    expect(screen.getByText(en.shell.empty)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.shell.signIn })).toBeNull();
  });

  it("renders nothing until the session check answers", async () => {
    let answer: (r: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>((resolve) => (answer = resolve))),
    );
    const { container, unmount } = render(
      <AuthGate>
        <p>inside</p>
      </AuthGate>,
    );
    expect(container).toBeEmptyDOMElement();
    // An answer after unmount changes nothing (and does not warn).
    unmount();
    await act(async () => {
      answer(new Response(JSON.stringify(signedIn.body)));
    });
    expect(container).toBeEmptyDOMElement();
  });
});
