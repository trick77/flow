import type { EventSourceLike } from "../api/stream";

// FakeEventSource stands in for the browser's EventSource (jsdom has none).
// Every instance is recorded; a test drives it with open, emit, fail.
export class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = [];

  static reset(): void {
    FakeEventSource.instances = [];
  }

  static last(): FakeEventSource {
    const es = FakeEventSource.instances[FakeEventSource.instances.length - 1];
    if (!es) throw new Error("no EventSource was created");
    return es;
  }

  static forUrl(prefix: string): FakeEventSource[] {
    return FakeEventSource.instances.filter((es) => es.url.startsWith(prefix));
  }

  readonly url: string;
  readyState = 0;
  closed = false;
  private listeners = new Map<string, ((ev: MessageEvent) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  close(): void {
    this.closed = true;
    this.readyState = 2;
  }

  private dispatch(type: string, data?: string, lastEventId = ""): void {
    const ev = new MessageEvent(type, { data, lastEventId });
    for (const l of this.listeners.get(type) ?? []) l(ev);
  }

  open(): void {
    this.readyState = 1;
    this.dispatch("open");
  }

  // emit sends one frame named type; data objects are JSON-encoded.
  emit(type: string, data: unknown): void {
    const text = typeof data === "string" ? data : JSON.stringify(data);
    const seq = (data as { seq?: number } | null)?.seq;
    this.dispatch(type, text, seq === undefined ? "" : String(seq));
  }

  // fail fires error; closed=true is the browser giving up (readyState 2),
  // false a reconnect it handles by itself (readyState 0).
  fail(closed: boolean): void {
    this.readyState = closed ? 2 : 0;
    this.dispatch("error");
  }
}
