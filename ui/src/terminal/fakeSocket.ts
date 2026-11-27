import type { WebSocketLike } from "./socket";

// FakeWebSocket records what the code under test sends and lets a test play
// the server: open, binary and text frames, close codes. Test-only helper.
export class FakeWebSocket implements WebSocketLike {
  static instances: FakeWebSocket[] = [];
  static reset() {
    FakeWebSocket.instances = [];
  }
  static last(): FakeWebSocket {
    const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    if (!ws) throw new Error("no socket was opened");
    return ws;
  }

  binaryType = "blob";
  readyState = 0;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  sent: (string | Uint8Array)[] = [];
  closedWith: number | undefined;

  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this);
  }

  send(data: string | ArrayBufferLike | ArrayBufferView) {
    this.sent.push(
      typeof data === "string" ? data : new Uint8Array(data as ArrayBuffer),
    );
  }

  close(code?: number) {
    this.closedWith = code;
    this.readyState = 3;
  }

  // Server side.
  open() {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }
  bytes(text: string) {
    const data = new TextEncoder().encode(text).buffer;
    this.onmessage?.(new MessageEvent("message", { data }));
  }
  text(text: string) {
    this.onmessage?.(new MessageEvent("message", { data: text }));
  }
  end(code: number) {
    this.readyState = 3;
    this.onerror?.(new Event("error"));
    this.onclose?.(new CloseEvent("close", { code }));
  }
  sentText(): string[] {
    return this.sent.filter((s): s is string => typeof s === "string");
  }
  sentBytes(): string {
    const dec = new TextDecoder();
    return this.sent
      .filter((s): s is Uint8Array => typeof s !== "string")
      .map((b) => dec.decode(b))
      .join("");
  }
}
