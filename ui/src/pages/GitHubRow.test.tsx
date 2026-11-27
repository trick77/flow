import { act, fireEvent, render, screen, within } from "@testing-library/react";

import { resources } from "../i18n/resources";
import type { GitHubConnection } from "../api/types.gen";
import { GitHubRow } from "./GitHubRow";

// conn is a connection on a server with a GitHub App unless said otherwise.
type Conn = Omit<GitHubConnection, "available"> & { available?: boolean };
const full = (c: Conn): GitHubConnection => ({ available: true, ...c });
import { forgetDeviceFlow } from "./useDeviceFlow";

const en = resources.en.settings;
const errors = resources.en.errors;
const T0 = Date.parse("2026-11-27T10:00:00Z");

const json = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });

const device = {
  userCode: "ABCD-1234",
  verificationUri: "https://github.com/login/device",
  expiresAt: new Date(T0 + 15 * 60_000).toISOString(),
  intervalSeconds: 5,
};

interface Routes {
  start?: Response;
  polls?: Response[];
  disconnect?: Response;
}

function serve(routes: Routes) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push(`${init?.method ?? "GET"} ${url}`);
      if (url === "/api/connections/github/device") {
        return routes.start ?? json(200, device);
      }
      if (url === "/api/connections/github/device/poll") {
        return routes.polls?.shift() ?? json(200, { state: "pending" });
      }
      if (url === "/api/connections/github") {
        return routes.disconnect ?? json(204);
      }
      throw new Error("unexpected " + url);
    }),
  );
  return calls;
}

function renderRow(connection: Conn) {
  const onConnected = vi.fn();
  const onChanged = vi.fn();
  render(
    <GitHubRow
      connection={full(connection)}
      onConnected={onConnected}
      onChanged={onChanged}
    />,
  );
  return {
    row: screen.getByRole("group", { name: en.github }),
    onConnected,
    onChanged,
  };
}

const advance = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: false });
  vi.setSystemTime(T0);
  forgetDeviceFlow();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(navigator, "clipboard");
});

describe("GitHubRow states", () => {
  it("unavailable: one line, no button", () => {
    serve({});
    const { row } = renderRow({ state: "disconnected", available: false });
    expect(row).toHaveTextContent(en.githubUnavailable);
    expect(within(row).getByRole("status")).toHaveTextContent(
      en.pillUnavailable,
    );
    expect(within(row).queryByRole("button")).toBeNull();
  });

  it("disconnected: Connect", () => {
    serve({});
    const { row } = renderRow({ state: "disconnected" });
    expect(row).toHaveTextContent(en.githubDisconnectedHint);
    expect(row).toHaveTextContent(en.pillNotConnected);
    expect(
      within(row).getByRole("button", { name: en.connect }),
    ).toBeInTheDocument();
  });

  it("connecting without a code here: Start again", () => {
    serve({});
    const { row } = renderRow({ state: "pending" });
    expect(row).toHaveTextContent(en.githubPendingHint);
    expect(row).toHaveTextContent(en.pillConnecting);
    expect(
      within(row).getByRole("button", { name: en.startAgain }),
    ).toBeInTheDocument();
  });

  it("connected: login, since, renew by, Disconnect", () => {
    serve({});
    const { row } = renderRow({
      state: "connected",
      login: "octo-user",
      connectedAt: "2026-11-20T09:00:00Z",
      refreshExpiresAt: "2027-05-20T09:00:00Z",
    });
    expect(row).toHaveTextContent(
      "Connected as octo-user · since 20.11.2026 · renew by 20.05.2027",
    );
    expect(row).toHaveTextContent(en.pillConnected);
    expect(
      within(row).getByRole("button", { name: en.disconnect }),
    ).toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: en.connect })).toBeNull();
  });

  it("connected without a login says only connected", () => {
    serve({});
    const { row } = renderRow({ state: "connected" });
    expect(row).toHaveTextContent(en.githubConnected);
  });

  it("expired: a warning and Reconnect", () => {
    serve({});
    const { row } = renderRow({ state: "expired", login: "octo-user" });
    expect(row).toHaveTextContent(en.githubExpiredHint);
    expect(row).toHaveTextContent(en.pillReconnect);
    expect(within(row).getByRole("button", { name: en.reconnect })).toHaveClass(
      "primary",
    );
    expect(
      within(row).getByRole("button", { name: en.disconnect }),
    ).toBeInTheDocument();
  });

  it("an unknown state reads as error", () => {
    serve({});
    const { row } = renderRow({ state: "odd" as "pending" });
    expect(row).toHaveTextContent(en.githubUnknownState);
    expect(row).toHaveTextContent(en.pillError);
  });
});

describe("GitHubRow device flow", () => {
  async function connect(routes: Routes = {}) {
    const calls = serve(routes);
    const r = renderRow({ state: "disconnected" });
    await act(async () => {
      fireEvent.click(within(r.row).getByRole("button", { name: en.connect }));
    });
    return { ...r, calls };
  }

  it("shows the code, the link and the countdown", async () => {
    const { row } = await connect();
    const panel = within(row).getByRole("region", { name: en.deviceTitle });
    expect(within(panel).getByLabelText(en.deviceCode)).toHaveTextContent(
      "ABCD-1234",
    );
    const link = within(panel).getByRole("link", { name: en.deviceOpen });
    expect(link).toHaveAttribute("href", "https://github.com/login/device");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(panel).toHaveTextContent("The code expires in 15:00.");
    expect(panel).toHaveTextContent(en.deviceWaiting);
    expect(row).toHaveTextContent(en.pillConnecting);
    // The panel says what to do; the row does not claim a stale start.
    expect(row).toHaveTextContent(en.githubDisconnectedHint);
    expect(row).not.toHaveTextContent(en.githubPendingHint);
    await advance(61_000);
    expect(panel).toHaveTextContent("The code expires in 13:59.");
  });

  it("drops a link that is not http(s)", async () => {
    const { row } = await connect({
      start: json(200, { ...device, verificationUri: "javascript:x" }),
    });
    expect(within(row).queryByRole("link")).toBeNull();
  });

  it("copies the code and says so", async () => {
    const write = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: write },
      configurable: true,
    });
    const { row } = await connect();
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: en.deviceCopy }));
    });
    expect(write).toHaveBeenCalledWith("ABCD-1234");
    expect(
      within(row).getByRole("button", { name: en.deviceCopied }),
    ).toBeInTheDocument();
    await advance(2000);
    expect(
      within(row).getByRole("button", { name: en.deviceCopy }),
    ).toBeInTheDocument();
  });

  it("selects the code when copying fails", async () => {
    Object.defineProperty(document, "execCommand", {
      value: () => false,
      configurable: true,
      writable: true,
    });
    const { row } = await connect();
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: en.deviceCopy }));
    });
    expect(within(row).getByRole("alert")).toHaveTextContent(
      en.deviceCopyFailed,
    );
    expect(window.getSelection()?.toString()).toBe("ABCD-1234");
  });

  it("success: says so and hands the connection up", async () => {
    const { row, onConnected } = await connect({
      polls: [json(200, { state: "connected", login: "octo-user" })],
    });
    await advance(5000);
    expect(within(row).queryByRole("region")).toBeNull();
    expect(row).toHaveTextContent("Connected as octo-user.");
    expect(onConnected).toHaveBeenCalledWith({
      state: "connected",
      login: "octo-user",
      available: true,
    });
  });

  it("success without a login", async () => {
    const { row } = await connect({
      polls: [json(200, { state: "connected" })],
    });
    await advance(5000);
    expect(row).toHaveTextContent(en.deviceConnected);
  });

  it("denied: says so and offers another try", async () => {
    const { row, calls } = await connect({
      polls: [json(200, { state: "denied" })],
    });
    await advance(5000);
    expect(within(row).getByRole("alert")).toHaveTextContent(en.deviceDenied);
    await act(async () => {
      fireEvent.click(within(row).getByRole("button", { name: en.tryAgain }));
    });
    expect(
      calls.filter((c) => c === "POST /api/connections/github/device"),
    ).toHaveLength(2);
    expect(within(row).getByRole("region")).toBeInTheDocument();
  });

  it("expired: says so", async () => {
    const { row } = await connect({ polls: [json(200, { state: "expired" })] });
    await advance(5000);
    expect(within(row).getByRole("alert")).toHaveTextContent(en.deviceExpired);
  });

  it("a failed start says why", async () => {
    const { row } = await connect({ start: json(502, { code: "upstream" }) });
    expect(within(row).getByRole("alert")).toHaveTextContent(
      `${en.deviceFailed} ${errors.upstream}`,
    );
  });

  it("a 501 at start shows the row as unavailable", async () => {
    const { row } = await connect({
      start: json(501, { code: "not_implemented" }),
    });
    expect(row).toHaveTextContent(en.githubUnavailable);
    expect(within(row).queryByRole("button")).toBeNull();
  });

  it("Cancel closes the panel and stops polling", async () => {
    const { row, calls } = await connect();
    fireEvent.click(within(row).getByRole("button", { name: en.cancel }));
    expect(within(row).queryByRole("region")).toBeNull();
    await advance(30_000);
    expect(calls.some((c) => c.endsWith("/poll"))).toBe(false);
  });

  it("says when polling paused in a hidden tab", async () => {
    let hidden = false;
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => hidden,
    });
    const { row } = await connect();
    hidden = true;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await advance(60_000);
    expect(row).toHaveTextContent(en.devicePaused);
    hidden = false;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(row).toHaveTextContent(en.deviceWaiting);
  });
});

describe("GitHubRow disconnect", () => {
  const connected: Conn = {
    state: "connected",
    login: "octo-user",
  };

  it("asks first, explaining the effect, then disconnects", async () => {
    const calls = serve({});
    const { row, onChanged } = renderRow(connected);
    fireEvent.click(within(row).getByRole("button", { name: en.disconnect }));
    const dialog = within(row).getByRole("alertdialog");
    expect(dialog).toHaveTextContent(en.confirmDisconnect);
    await act(async () => {
      fireEvent.click(
        within(dialog).getByRole("button", { name: en.disconnect }),
      );
    });
    expect(calls).toEqual(["DELETE /api/connections/github"]);
    expect(onChanged).toHaveBeenCalled();
    expect(within(row).queryByRole("alertdialog")).toBeNull();
  });

  it("Cancel keeps the connection", () => {
    const calls = serve({});
    const { row } = renderRow(connected);
    fireEvent.click(within(row).getByRole("button", { name: en.disconnect }));
    fireEvent.click(within(row).getByRole("button", { name: en.cancel }));
    expect(within(row).queryByRole("alertdialog")).toBeNull();
    expect(calls).toEqual([]);
  });

  it("says why a disconnect failed", async () => {
    serve({ disconnect: json(501, { code: "not_implemented" }) });
    const { row, onChanged } = renderRow(connected);
    fireEvent.click(within(row).getByRole("button", { name: en.disconnect }));
    await act(async () => {
      fireEvent.click(
        within(within(row).getByRole("alertdialog")).getByRole("button", {
          name: en.disconnect,
        }),
      );
    });
    expect(within(row).getByRole("alert")).toHaveTextContent(
      errors.not_implemented,
    );
    expect(onChanged).not.toHaveBeenCalled();
  });
});
