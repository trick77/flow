import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { useState } from "react";

import type { SessionSummary } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { SessionView } from "../shell";
import { FakeEventSource } from "../test/fakeEventSource";
import type { Emulator } from "./emulator";
import { FakeWebSocket } from "./fakeSocket";
import { TerminalDrawer, type TerminalTab } from "./TerminalDrawer";

const en = resources.en.terminal;

function reply(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
  });
}

const created = {
  id: "s-s1-1",
  projectId: "p1",
  sessionId: "s1",
  cwd: "/home/flow/work/.sessions/flaky",
};

// FakeEmulator stands in for xterm.js: it records writes and lets a test type.
class FakeEmulator implements Emulator {
  static all: FakeEmulator[] = [];
  text = "";
  size: { cols: number; rows: number } | null = { cols: 100, rows: 30 };
  focused = 0;
  disposed = false;
  private data: ((d: string) => void) | null = null;
  constructor(readonly el: HTMLElement) {
    FakeEmulator.all.push(this);
  }
  write(b: Uint8Array) {
    this.text += new TextDecoder().decode(b);
  }
  reset() {
    this.text = "";
  }
  fit() {
    return this.size;
  }
  focus() {
    this.focused++;
  }
  onData(fn: (d: string) => void) {
    this.data = fn;
  }
  type(d: string) {
    this.data?.(d);
  }
  dispose() {
    this.disposed = true;
  }
}

const tab: TerminalTab = { key: "s1-1", projectId: "p1", sessionId: "s1" };

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen((o) => !o)}>
        toggle
      </button>
      <TerminalDrawer
        tab={tab}
        open={open}
        onClose={() => setOpen(false)}
        WebSocket={FakeWebSocket}
        createEmulator={(el) => new FakeEmulator(el)}
      />
    </>
  );
}

describe("TerminalDrawer", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockImplementation(() => Promise.resolve(reply(201, created)));
    vi.stubGlobal("fetch", fetchMock);
    FakeWebSocket.reset();
    FakeEmulator.all = [];
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function openDrawer() {
    fireEvent.click(screen.getByRole("button", { name: "toggle" }));
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const ws = FakeWebSocket.last();
    act(() => ws.open());
    return ws;
  }

  it("opens on demand, shows the directory and relays keystrokes", async () => {
    render(<Harness />);
    expect(screen.queryByRole("region", { name: en.title })).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    const ws = await openDrawer();
    const region = screen.getByRole("region", { name: en.title });
    expect(region).toBeVisible();
    expect(region).toHaveAttribute("data-status", "live");
    expect(screen.getByText(created.cwd)).toBeInTheDocument();
    expect(ws.url).toContain("/api/terminals/s-s1-1/ws?cols=100&rows=30");
    const emu = FakeEmulator.all[0];
    act(() => ws.bytes("$ "));
    expect(emu.text).toBe("$ ");
    emu.type("ls\r");
    expect(ws.sentBytes()).toBe("ls\r");
    expect(emu.focused).toBeGreaterThan(0);
  });

  it("keeps its content while hidden and fits again when shown", async () => {
    render(<Harness />);
    const ws = await openDrawer();
    const emu = FakeEmulator.all[0];
    act(() => ws.bytes("kept output"));
    const region = screen.getByRole("region", { name: en.title });
    fireEvent.click(screen.getByRole("button", { name: en.close }));
    expect(region).not.toBeVisible();
    expect(emu.disposed).toBe(false);
    emu.size = { cols: 90, rows: 20 };
    fireEvent.click(screen.getByRole("button", { name: "toggle" }));
    expect(region).toBeVisible();
    expect(emu.text).toBe("kept output");
    expect(FakeEmulator.all).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    // The new size reaches the terminal; the same size again sends nothing.
    expect(ws.sentText()).toContain('{"resize":{"cols":90,"rows":20}}');
    const n = ws.sentText().length;
    fireEvent.click(screen.getByRole("button", { name: "toggle" }));
    fireEvent.click(screen.getByRole("button", { name: "toggle" }));
    expect(ws.sentText()).toHaveLength(n);
    // Hidden: nothing measured, nothing sent.
    fireEvent.click(screen.getByRole("button", { name: "toggle" }));
    emu.size = null;
    fireEvent.click(screen.getByRole("button", { name: "toggle" }));
    expect(ws.sentText()).toHaveLength(n);
  });

  it("says when it reconnects and replays the scrollback after", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<Harness />);
    const ws = await openDrawer();
    const emu = FakeEmulator.all[0];
    act(() => ws.bytes("before"));
    act(() => ws.end(1012));
    expect(screen.getByRole("status")).toHaveTextContent(en.disconnected);
    act(() => vi.advanceTimersByTime(600));
    const again = FakeWebSocket.last();
    expect(again).not.toBe(ws);
    act(() => again.open());
    act(() => again.bytes("before"));
    expect(emu.text).toBe("before");
    expect(screen.queryByRole("status")).toBeNull();
    vi.useRealTimers();
  });

  it("shows the exit status and restarts on request", async () => {
    render(<Harness />);
    const ws = await openDrawer();
    act(() => {
      ws.text('{"exit":{"code":130}}');
      ws.end(1000);
    });
    expect(screen.getByRole("status")).toHaveTextContent(
      "Exited with status 130",
    );
    expect(screen.getByRole("region", { name: en.title })).toHaveAttribute(
      "data-status",
      "exited",
    );
    fireEvent.click(screen.getByRole("button", { name: en.restart }));
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(2));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    act(() => FakeWebSocket.last().open());
    expect(screen.queryByRole("button", { name: en.restart })).toBeNull();
  });

  it("names a gone terminal, a stopped sandbox and a refused create", async () => {
    render(<Harness />);
    const ws = await openDrawer();
    act(() => ws.end(4404));
    expect(screen.getByRole("status")).toHaveTextContent(en.gone);
    fetchMock.mockImplementationOnce(() =>
      Promise.resolve(reply(409, { code: "sandbox_memory_full" })),
    );
    fireEvent.click(screen.getByRole("button", { name: en.restart }));
    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent(
        resources.en.errors.sandbox_memory_full,
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: en.restart }));
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(2));
    act(() => FakeWebSocket.last().end(4409));
    expect(screen.getByRole("status")).toHaveTextContent(en.stopped);
  });

  it("detaches and frees the emulator when unmounted", async () => {
    const { unmount } = render(<Harness />);
    const ws = await openDrawer();
    unmount();
    expect(ws.closedWith).toBe(1000);
    expect(FakeEmulator.all[0].disposed).toBe(true);
  });
});

describe("SessionView terminal toggle", () => {
  const session: SessionSummary = {
    id: "s1",
    projectId: "p1",
    title: "Fix flaky test",
    provider: "claude",
    model: "claude-opus",
    mode: "ask",
    status: "idle",
    unseen: false,
    repos: ["loom"],
    branch: "flow/fix",
    lastActivityAt: "2026-11-27T08:00:00Z",
    createdAt: "2026-11-27T08:00:00Z",
  } as SessionSummary;

  beforeEach(() => {
    FakeEventSource.reset();
    FakeWebSocket.reset();
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(reply(201, created))),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("toggles the drawer from the header", async () => {
    render(
      <SessionView
        session={session}
        EventSource={FakeEventSource}
        WebSocket={FakeWebSocket}
        createEmulator={(el) => new FakeEmulator(el)}
      />,
    );
    const toggle = screen.getByRole("button", { name: en.toggle });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    expect(screen.getByRole("region", { name: en.title })).toBeVisible();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "false");
  });
});
