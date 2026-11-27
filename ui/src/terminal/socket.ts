import { ApiError, request } from "../api";
import type {
  ErrorCode,
  Terminal,
  TerminalControl,
  TerminalCreate,
  TerminalEvent,
} from "../api/types.gen";

// The browser side of a terminal: create-or-attach over the API, then the
// WebSocket (binary frames are the terminal's bytes, text frames TerminalControl
// out and TerminalEvent in). A dropped socket is attached again with backoff and
// the scrollback comes again, so the emulator is reset before each replay.

export interface WebSocketLike {
  binaryType: string;
  readonly readyState: number;
  onopen: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  onerror: ((ev: Event) => void) | null;
  send(data: string | ArrayBufferLike | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
}

export type WebSocketCtor = new (url: string) => WebSocketLike;

// Close codes the server uses (backend internal/terminals/wsterm).
export const CLOSE_NORMAL = 1000;
export const CLOSE_GONE = 4404;
export const CLOSE_NO_SANDBOX = 4409;

const OPEN = 1;

export type TerminalStatus =
  | { kind: "idle" }
  | { kind: "connecting" }
  | { kind: "live" }
  | { kind: "reconnecting" }
  | { kind: "exited"; code: number }
  | { kind: "gone" }
  | { kind: "stopped" }
  | { kind: "failed"; code: ErrorCode };

export interface TerminalSessionOptions {
  projectId: string;
  sessionId?: string;
  // The emulator's size now; null while it cannot be measured (hidden).
  size: () => { cols: number; rows: number } | null;
  // Bytes for the emulator; reset clears it before a scrollback replay.
  write: (bytes: Uint8Array) => void;
  reset: () => void;
  onStatus: (status: TerminalStatus) => void;
  onTerminal?: (t: Terminal) => void;
  WebSocket?: WebSocketCtor;
  // First wait before attaching again; doubles up to maxBackoffMs.
  backoffMs?: number;
  maxBackoffMs?: number;
  // Where the page is served from, for the ws(s) URL; window.location by default.
  location?: { protocol: string; host: string };
}

const FALLBACK = { cols: 80, rows: 24 };

function resolveCtor(ctor?: WebSocketCtor): WebSocketCtor {
  if (ctor) return ctor;
  return (globalThis as unknown as { WebSocket: WebSocketCtor }).WebSocket;
}

export class TerminalSession {
  private readonly o: TerminalSessionOptions;
  private ws: WebSocketLike | null = null;
  private terminal: Terminal | null = null;
  private status: TerminalStatus = { kind: "idle" };
  private timer: ReturnType<typeof setTimeout> | null = null;
  private delay: number;
  private disposed = false;
  private readonly encoder = new TextEncoder();

  constructor(o: TerminalSessionOptions) {
    this.o = o;
    this.delay = o.backoffMs ?? 500;
  }

  get current(): TerminalStatus {
    return this.status;
  }

  get info(): Terminal | null {
    return this.terminal;
  }

  private set(s: TerminalStatus) {
    this.status = s;
    this.o.onStatus(s);
  }

  // open creates the terminal (or finds the running one) and attaches. It is
  // also the restart: an ended terminal is started again by the same call.
  async open(): Promise<void> {
    this.clearTimer();
    this.closeSocket();
    this.set({ kind: "connecting" });
    const size = this.o.size() ?? FALLBACK;
    const body: TerminalCreate = {
      projectId: this.o.projectId,
      cols: size.cols,
      rows: size.rows,
    };
    if (this.o.sessionId) body.sessionId = this.o.sessionId;
    let t: Terminal;
    try {
      t = await request<Terminal>("POST", "/api/terminals", body);
    } catch (err) {
      if (this.disposed) return;
      this.set({
        kind: "failed",
        code: err instanceof ApiError ? err.code : "internal",
      });
      return;
    }
    if (this.disposed) return;
    this.terminal = t;
    this.o.onTerminal?.(t);
    this.delay = this.o.backoffMs ?? 500;
    this.connect();
  }

  private url(): string {
    const loc = this.o.location ?? window.location;
    const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
    const id = encodeURIComponent(this.terminal?.id ?? "");
    let u = `${scheme}//${loc.host}/api/terminals/${id}/ws`;
    const size = this.o.size();
    if (size) u += `?cols=${size.cols}&rows=${size.rows}`;
    return u;
  }

  private connect() {
    if (this.disposed || !this.terminal) return;
    const Ctor = resolveCtor(this.o.WebSocket);
    const ws = new Ctor(this.url());
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    let first = true;
    let exited: number | null = null;
    ws.onopen = () => {
      if (this.ws !== ws) return;
      this.delay = this.o.backoffMs ?? 500;
      this.set({ kind: "live" });
    };
    ws.onmessage = (ev: MessageEvent) => {
      if (this.ws !== ws) return;
      if (typeof ev.data === "string") {
        let msg: TerminalEvent;
        try {
          msg = JSON.parse(ev.data) as TerminalEvent;
        } catch {
          return;
        }
        if (msg.exit) exited = msg.exit.code;
        return;
      }
      if (first) {
        // The scrollback comes first on every attach: what the emulator holds
        // from before would show twice.
        first = false;
        this.o.reset();
      }
      this.o.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onclose = (ev: CloseEvent) => {
      if (this.ws !== ws) return;
      this.ws = null;
      if (this.disposed) return;
      if (exited !== null) {
        this.set({ kind: "exited", code: exited });
      } else if (ev.code === CLOSE_GONE) {
        this.set({ kind: "gone" });
      } else if (ev.code === CLOSE_NO_SANDBOX) {
        this.set({ kind: "stopped" });
      } else {
        this.retry();
      }
    };
    ws.onerror = () => {
      // onclose follows and decides.
    };
  }

  private retry() {
    this.set({ kind: "reconnecting" });
    const wait = this.delay;
    this.delay = Math.min(this.delay * 2, this.o.maxBackoffMs ?? 10_000);
    this.timer = setTimeout(() => {
      this.timer = null;
      this.connect();
    }, wait);
  }

  // send passes keystrokes (and pastes) to the shell, while attached.
  send(data: string) {
    if (this.ws && this.ws.readyState === OPEN) {
      this.ws.send(this.encoder.encode(data));
    }
  }

  // resize tells the terminal the emulator's size, while attached.
  resize(cols: number, rows: number) {
    if (this.ws && this.ws.readyState === OPEN) {
      const msg: TerminalControl = { resize: { cols, rows } };
      this.ws.send(JSON.stringify(msg));
    }
  }

  private clearTimer() {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private closeSocket() {
    const ws = this.ws;
    this.ws = null;
    if (ws) ws.close(CLOSE_NORMAL);
  }

  // dispose detaches for good; the terminal keeps running in the sandbox.
  dispose() {
    this.disposed = true;
    this.clearTimer();
    this.closeSocket();
  }
}
