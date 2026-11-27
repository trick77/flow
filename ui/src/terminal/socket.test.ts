import { FakeWebSocket } from "./fakeSocket";
import {
  CLOSE_GONE,
  CLOSE_NO_SANDBOX,
  TerminalSession,
  type TerminalStatus,
} from "./socket";

function reply(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
  });
}

const terminal = {
  id: "s-1-1",
  projectId: "p1",
  sessionId: "s1",
  cwd: "/home/flow/work/.sessions/x",
};

describe("TerminalSession", () => {
  const fetchMock = vi.fn<typeof fetch>();
  let statuses: TerminalStatus["kind"][];
  let written: string;
  let resets: number;
  let size: { cols: number; rows: number } | null;

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    FakeWebSocket.reset();
    statuses = [];
    written = "";
    resets = 0;
    size = { cols: 100, rows: 30 };
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  function make(
    extra: Partial<ConstructorParameters<typeof TerminalSession>[0]> = {},
  ) {
    const dec = new TextDecoder();
    return new TerminalSession({
      projectId: "p1",
      sessionId: "s1",
      size: () => size,
      write: (b) => (written += dec.decode(b)),
      reset: () => {
        resets++;
        written = "";
      },
      onStatus: (s) => statuses.push(s.kind),
      WebSocket: FakeWebSocket,
      location: { protocol: "https:", host: "flow.example" },
      backoffMs: 100,
      maxBackoffMs: 250,
      ...extra,
    });
  }

  it("creates, attaches with the size and relays both ways", async () => {
    fetchMock.mockResolvedValueOnce(reply(201, terminal));
    const onTerminal = vi.fn();
    const ts = make({ onTerminal });
    await ts.open();
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init?.body as string)).toEqual({
      projectId: "p1",
      sessionId: "s1",
      cols: 100,
      rows: 30,
    });
    expect(onTerminal).toHaveBeenCalledWith(terminal);
    expect(ts.info).toEqual(terminal);
    const ws = FakeWebSocket.last();
    expect(ws.url).toBe(
      "wss://flow.example/api/terminals/s-1-1/ws?cols=100&rows=30",
    );
    expect(ws.binaryType).toBe("arraybuffer");
    ts.send("ignored before open");
    ws.open();
    expect(ts.current.kind).toBe("live");
    ws.bytes("scrollback ");
    ws.bytes("live");
    expect(written).toBe("scrollback live");
    expect(resets).toBe(1); // once, before the replay
    ts.send("ls\r");
    ts.resize(120, 40);
    expect(ws.sentBytes()).toBe("ls\r");
    expect(ws.sentText()).toEqual(['{"resize":{"cols":120,"rows":40}}']);
    ws.text("not json");
    ws.text("{}");
    expect(statuses).toEqual(["connecting", "live"]);
    ts.dispose();
    expect(ws.closedWith).toBe(1000);
  });

  it("attaches again after a drop and replays into a cleared emulator", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValueOnce(reply(201, terminal));
    const ts = make();
    await ts.open();
    const first = FakeWebSocket.last();
    first.open();
    first.bytes("$ echo hi\r\nhi\r\n");
    first.end(1012); // Flow restarted
    expect(ts.current.kind).toBe("reconnecting");
    vi.advanceTimersByTime(100);
    const second = FakeWebSocket.last();
    expect(second).not.toBe(first);
    second.end(1006); // still down: the wait doubles, capped
    vi.advanceTimersByTime(199);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    const third = FakeWebSocket.last();
    third.open();
    third.bytes("$ echo hi\r\nhi\r\n");
    expect(written).toBe("$ echo hi\r\nhi\r\n");
    expect(resets).toBe(2);
    // No second POST: attaching again needs only the id.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    // A stale socket's late events change nothing.
    first.bytes("stale");
    first.end(1000);
    expect(written).not.toContain("stale");
    ts.dispose();
  });

  it("ends on an exit message with its status, and restarts with open", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(reply(201, terminal)));
    const ts = make();
    await ts.open();
    const ws = FakeWebSocket.last();
    ws.open();
    ws.text('{"exit":{"code":3}}');
    ws.end(1000);
    expect(ts.current).toEqual({ kind: "exited", code: 3 });
    await ts.open();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    FakeWebSocket.last().open();
    expect(ts.current.kind).toBe("live");
    ts.dispose();
  });

  it("tells a gone terminal and a stopped sandbox apart, without retrying", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(() => Promise.resolve(reply(201, terminal)));
    const ts = make();
    await ts.open();
    FakeWebSocket.last().end(CLOSE_GONE);
    expect(ts.current.kind).toBe("gone");
    await ts.open();
    FakeWebSocket.last().end(CLOSE_NO_SANDBOX);
    expect(ts.current.kind).toBe("stopped");
    vi.advanceTimersByTime(10_000);
    expect(FakeWebSocket.instances).toHaveLength(2);
    ts.dispose();
  });

  it("reports a refused create by its error code", async () => {
    fetchMock.mockResolvedValueOnce(
      reply(409, { code: "sandbox_memory_full" }),
    );
    const ts = make();
    await ts.open();
    expect(ts.current).toEqual({ kind: "failed", code: "sandbox_memory_full" });
    fetchMock.mockRejectedValueOnce(new TypeError("offline"));
    await ts.open();
    expect(ts.current).toEqual({ kind: "failed", code: "internal" });
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("uses a fallback size while hidden and plain ws on http", async () => {
    size = null;
    fetchMock.mockResolvedValueOnce(reply(201, terminal));
    const ts = make({
      sessionId: undefined,
      location: { protocol: "http:", host: "127.0.0.1:8080" },
    });
    await ts.open();
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init?.body as string)).toEqual({
      projectId: "p1",
      cols: 80,
      rows: 24,
    });
    expect(FakeWebSocket.last().url).toBe(
      "ws://127.0.0.1:8080/api/terminals/s-1-1/ws",
    );
    ts.dispose();
  });

  it("stops after dispose, also mid-create", async () => {
    let answer: (r: Response) => void = () => {};
    fetchMock.mockReturnValueOnce(new Promise((r) => (answer = r)));
    const ts = make();
    const opening = ts.open();
    ts.dispose();
    answer(reply(201, terminal));
    await opening;
    expect(FakeWebSocket.instances).toHaveLength(0);
    fetchMock.mockReturnValueOnce(Promise.reject(new TypeError("x")));
    const ts2 = make();
    const o2 = ts2.open();
    ts2.dispose();
    await o2;
    expect(ts2.current.kind).toBe("connecting");
  });

  it("does not retry once disposed while a drop is pending", async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValueOnce(reply(201, terminal));
    const ts = make();
    await ts.open();
    const ws = FakeWebSocket.last();
    ws.open();
    ws.end(1011);
    ts.dispose();
    vi.advanceTimersByTime(1000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("finds the browser's WebSocket by itself", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    fetchMock.mockResolvedValueOnce(reply(201, terminal));
    const ts = make({ WebSocket: undefined, location: undefined });
    await ts.open();
    expect(FakeWebSocket.last().url).toMatch(
      /^ws:\/\/.*\/api\/terminals\/s-1-1\/ws/,
    );
    ts.dispose();
  });
});
