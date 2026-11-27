import { act, renderHook } from "@testing-library/react";

import {
  forgetDeviceFlow,
  HIDDEN_GRACE_MS,
  useDeviceFlow,
} from "./useDeviceFlow";

const T0 = Date.parse("2026-11-27T10:00:00Z");

const json = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });

const device = (over: Record<string, unknown> = {}) => ({
  userCode: "ABCD-1234",
  verificationUri: "https://github.com/login/device",
  expiresAt: new Date(T0 + 15 * 60_000).toISOString(),
  expiresInSeconds: 900,
  intervalSeconds: 5,
  ...over,
});

// serve answers the start with `start` and each poll with the next of
// `polls` (pending once they run out; an Error is a request that got no
// answer); polls records when each poll came.
function serve(
  start: Response | (() => Promise<Response>),
  polls: (Response | Error)[],
) {
  const pollTimes: number[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/connections/github/device") {
        return typeof start === "function" ? start() : start;
      }
      if (url === "/api/connections/github/device/poll") {
        pollTimes.push(Date.now() - T0);
        const next = polls.shift() ?? json(200, { state: "pending" });
        if (next instanceof Error) throw next;
        return next;
      }
      throw new Error("unexpected " + url);
    }),
  );
  return pollTimes;
}

let hidden = false;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(T0);
  forgetDeviceFlow();
  hidden = false;
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const advance = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

async function started(onConnected = vi.fn()) {
  const hook = renderHook(() => useDeviceFlow(onConnected));
  await act(async () => {
    hook.result.current.start();
  });
  return hook;
}

describe("useDeviceFlow", () => {
  it("polls at the server's interval, counted from each answer", async () => {
    const times = serve(json(200, device()), []);
    const { result } = await started();
    expect(result.current.state.phase).toBe("waiting");
    await advance(4999);
    expect(times).toEqual([]);
    await advance(1);
    expect(times).toEqual([5000]);
    await advance(10_000);
    expect(times).toEqual([5000, 10_000, 15_000]);
    expect(result.current.remainingMs).toBe(15 * 60_000 - 15_000);
  });

  it("lengthens the interval on a slow-down and keeps it", async () => {
    const times = serve(json(200, device()), [
      json(200, { state: "slow_down" }),
      json(200, { state: "pending", intervalSeconds: 20 }),
      json(200, { state: "pending", intervalSeconds: 3 }),
    ]);
    await started();
    await advance(5000);
    expect(times).toEqual([5000]);
    // 5 s more after a bare slow-down.
    await advance(9999);
    expect(times).toEqual([5000]);
    await advance(1);
    expect(times).toEqual([5000, 15_000]);
    // The server asked for 20 s.
    await advance(20_000);
    expect(times).toEqual([5000, 15_000, 35_000]);
    // A shorter interval later never speeds it up.
    await advance(19_999);
    expect(times).toHaveLength(3);
    await advance(1);
    expect(times).toEqual([5000, 15_000, 35_000, 55_000]);
  });

  it("stops on success and hands over the connection", async () => {
    const onConnected = vi.fn();
    const times = serve(json(200, device()), [
      json(200, { state: "pending" }),
      json(200, { state: "connected", login: "octo-user" }),
    ]);
    const { result } = await started(onConnected);
    await advance(10_000);
    expect(result.current.state).toEqual({
      phase: "connected",
      connection: { state: "connected", login: "octo-user", available: true },
    });
    expect(onConnected).toHaveBeenCalledWith({
      state: "connected",
      login: "octo-user",
      available: true,
    });
    await advance(60_000);
    expect(times).toHaveLength(2);
  });

  it("stops on a denial and on the server's expiry", async () => {
    let times = serve(json(200, device()), [
      json(200, { state: "disconnected" }),
    ]);
    let hook = await started();
    await advance(5000);
    expect(hook.result.current.state.phase).toBe("denied");
    await advance(30_000);
    expect(times).toHaveLength(1);
    hook.unmount();

    times = serve(json(200, device()), [json(200, { state: "expired" })]);
    hook = await started();
    await advance(5000);
    expect(hook.result.current.state.phase).toBe("expired");
    await advance(30_000);
    expect(times).toHaveLength(1);
  });

  it("counts the code's time from the seconds left, not the server's clock", async () => {
    // The browser's clock runs minutes ahead of the server's: by the
    // absolute expiresAt the code would be over already.
    const times = serve(
      json(
        200,
        device({
          expiresAt: new Date(T0 - 5 * 60_000).toISOString(),
          expiresInSeconds: 12,
        }),
      ),
      [],
    );
    const { result } = await started();
    expect(result.current.state.phase).toBe("waiting");
    await advance(11_000);
    expect(times).toEqual([5000, 10_000]);
    expect(result.current.remainingMs).toBe(1000);
    // The local deadline passes: the countdown ends, the server decides.
    await advance(1000);
    expect(result.current.remainingMs).toBe(0);
    expect(result.current.state.phase).toBe("waiting");
  });

  it("ends on the server's expired answer, or a lost answer past the deadline", async () => {
    let times = serve(json(200, device({ expiresInSeconds: 8 })), [
      json(200, { state: "pending" }),
      json(200, { state: "expired" }),
    ]);
    let hook = await started();
    await advance(10_000);
    expect(hook.result.current.state.phase).toBe("expired");
    await advance(30_000);
    expect(times).toHaveLength(2);
    hook.unmount();

    times = serve(json(200, device({ expiresInSeconds: 8 })), [
      json(200, { state: "pending" }),
      new TypeError("network"),
    ]);
    hook = await started();
    await advance(10_000);
    expect(hook.result.current.state.phase).toBe("expired");
    await advance(30_000);
    expect(times).toHaveLength(2);
  });

  it("retries a lost answer at the same interval and stops on a refusal", async () => {
    const times = serve(json(200, device()), [
      new TypeError("network"),
      json(409, { code: "conflict" }),
    ]);
    const { result } = await started();
    await advance(5000);
    expect(result.current.state.phase).toBe("waiting");
    await advance(5000);
    expect(times).toEqual([5000, 10_000]);
    expect(result.current.state).toEqual({ phase: "failed", code: "conflict" });
  });

  it("ends on an upstream or internal answer: the server's code is over", async () => {
    for (const code of ["upstream", "internal"] as const) {
      const times = serve(json(200, device()), [json(502, { code })]);
      const hook = await started();
      await advance(5000);
      expect(hook.result.current.state).toEqual({ phase: "failed", code });
      await advance(30_000);
      expect(times).toHaveLength(1);
      hook.unmount();
    }
  });

  it("reads a 501 as unavailable, at start and while polling", async () => {
    serve(json(501, { code: "not_implemented" }), []);
    let hook = await started();
    expect(hook.result.current.state.phase).toBe("unavailable");
    hook.unmount();

    serve(json(200, device()), [json(501, { code: "not_implemented" })]);
    hook = await started();
    await advance(5000);
    expect(hook.result.current.state.phase).toBe("unavailable");
  });

  it("says why a start failed", async () => {
    serve(json(502, { code: "upstream" }), []);
    const { result } = await started();
    expect(result.current.state).toEqual({ phase: "failed", code: "upstream" });
    act(() => result.current.reset());
    expect(result.current.state.phase).toBe("idle");
  });

  it("cancel stops polling and drops a start still under way", async () => {
    const times = serve(json(200, device()), []);
    const { result } = await started();
    await advance(5000);
    act(() => result.current.cancel());
    expect(result.current.state.phase).toBe("idle");
    await advance(30_000);
    expect(times).toEqual([5000]);

    let answer: (r: Response) => void = () => undefined;
    serve(() => new Promise<Response>((r) => (answer = r)), []);
    act(() => result.current.start());
    expect(result.current.state.phase).toBe("starting");
    act(() => result.current.cancel());
    await act(async () => answer(json(200, device())));
    expect(result.current.state.phase).toBe("idle");
  });

  it("stops on unmount and resumes the same code when shown again", async () => {
    const times = serve(json(200, device()), []);
    const first = await started();
    await advance(5000);
    first.unmount();
    await advance(60_000);
    expect(times).toEqual([5000]);
    const again = renderHook(() => useDeviceFlow(vi.fn()));
    expect(again.result.current.state).toMatchObject({
      phase: "waiting",
      flow: { userCode: "ABCD-1234" },
    });
    // A full interval first: never faster than the server said.
    await advance(4999);
    expect(times).toEqual([5000]);
    await advance(1);
    expect(times).toEqual([5000, 70_000]);
    // Cancel forgets it.
    act(() => again.result.current.cancel());
    again.unmount();
    expect(
      renderHook(() => useDeviceFlow(vi.fn())).result.current.state,
    ).toEqual({ phase: "idle" });
  });

  it("forgets a kept code that expired meanwhile", async () => {
    serve(
      json(
        200,
        device({
          expiresAt: new Date(T0 + 20_000).toISOString(),
          expiresInSeconds: 20,
        }),
      ),
      [],
    );
    const first = await started();
    first.unmount();
    vi.setSystemTime(T0 + 30_000);
    expect(
      renderHook(() => useDeviceFlow(vi.fn())).result.current.state,
    ).toEqual({ phase: "idle" });
  });

  it("an unmount drops a start still under way", async () => {
    let answer: (r: Response) => void = () => undefined;
    serve(() => new Promise<Response>((r) => (answer = r)), []);
    const hook = renderHook(() => useDeviceFlow(vi.fn()));
    act(() => hook.result.current.start());
    hook.unmount();
    await act(async () => answer(json(200, device())));
    expect(
      renderHook(() => useDeviceFlow(vi.fn())).result.current.state,
    ).toEqual({ phase: "idle" });
  });

  it("pauses in a tab hidden for long and resumes when visible", async () => {
    const times = serve(json(200, device()), []);
    const { result } = await started();
    await advance(5000);
    hidden = true;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    // Polling goes on during the grace period.
    await advance(HIDDEN_GRACE_MS);
    expect(result.current.paused).toBe(true);
    const during = times.length;
    // The poll due as the grace ends is not sent: the pause came first.
    expect(during).toBe(HIDDEN_GRACE_MS / 5000);
    await advance(120_000);
    expect(times).toHaveLength(during);
    hidden = false;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(result.current.paused).toBe(false);
    // The interval since the last answer is long over: polled at once.
    await advance(0);
    expect(times).toHaveLength(during + 1);
  });

  it("a short hide does not pause", async () => {
    const times = serve(json(200, device()), []);
    const { result } = await started();
    hidden = true;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await advance(10_000);
    hidden = false;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await advance(HIDDEN_GRACE_MS);
    expect(result.current.paused).toBe(false);
    expect(times).toHaveLength(2 + HIDDEN_GRACE_MS / 5000);
  });
});
