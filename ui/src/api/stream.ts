import { isDurable, parseEvent } from "../contract/events";
import type { TypedEvent } from "../contract/events.gen";
import type { AppEvent } from "./types.gen";

// The part of EventSource the streams use; tests inject a fake (jsdom has no
// EventSource).
export interface EventSourceLike {
  readonly readyState: number;
  addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
  close(): void;
}

export type EventSourceCtor = new (url: string) => EventSourceLike;

// EventSource.CLOSED: the browser gave up (a non-200 answer, a dropped
// connection it will not retry); anything else it retries by itself with
// Last-Event-ID.
const CLOSED = 2;

export interface StreamOptions {
  EventSource?: EventSourceCtor;
  // First wait before recreating a closed stream; doubles up to maxBackoffMs.
  backoffMs?: number;
  maxBackoffMs?: number;
}

interface GenericStream<T> extends StreamOptions {
  url: (lastSeq: number) => string;
  eventName: string;
  parse: (data: string) => T | null;
  seqOf: (value: T) => number | undefined;
  onEvent: (value: T) => void;
  onOpen?: () => void;
  after?: number;
}

function resolveCtor(ctor?: EventSourceCtor): EventSourceCtor | undefined {
  if (ctor) return ctor;
  // Looked up late: a bare EventSource reference throws where there is none.
  const g = globalThis as { EventSource?: EventSourceCtor };
  return g.EventSource;
}

// openStream keeps one EventSource open on url and hands every frame named
// eventName to onEvent. Frames with a seq are delivered once, in order: a seq
// at or below the last one seen is dropped (a replay after reconnect). When the
// browser gives up, the stream is recreated after a backoff from the last seq.
// The returned function closes it for good; later callbacks are muted.
function openStream<T>(o: GenericStream<T>): () => void {
  const Ctor = resolveCtor(o.EventSource);
  const base = o.backoffMs ?? 1000;
  const max = o.maxBackoffMs ?? 30000;
  let lastSeq = o.after ?? 0;
  let wait = base;
  let closed = false;
  let source: EventSourceLike | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const connect = () => {
    if (closed || !Ctor) return;
    const es = new Ctor(o.url(lastSeq));
    source = es;
    es.addEventListener("open", () => {
      if (closed || source !== es) return;
      wait = base;
      o.onOpen?.();
    });
    es.addEventListener(o.eventName, (ev) => {
      if (closed || source !== es) return;
      let value: T | null;
      try {
        value = o.parse(String(ev.data));
      } catch {
        return;
      }
      if (value === null) return;
      const seq = o.seqOf(value);
      if (seq !== undefined) {
        if (seq <= lastSeq) return;
        lastSeq = seq;
      }
      o.onEvent(value);
    });
    es.addEventListener("error", () => {
      if (closed || source !== es || es.readyState !== CLOSED) return;
      es.close();
      timer = setTimeout(() => {
        timer = undefined;
        connect();
      }, wait);
      wait = Math.min(wait * 2, max);
    });
  };

  connect();
  return () => {
    closed = true;
    if (timer !== undefined) clearTimeout(timer);
    source?.close();
  };
}

export interface SessionStreamOptions extends StreamOptions {
  // Resume after this seq (default 0: the whole log).
  after?: number;
  onEvent: (event: TypedEvent) => void;
  // Each (re)open: live blocks are stale until the next snapshot or delta.
  onOpen?: () => void;
}

// streamSessionEvents follows GET /api/sessions/{id}/events: SSE frames named
// "flow", one Flow event each; durable events deduped by seq, ephemeral ones
// passed through.
export function streamSessionEvents(
  sessionId: string,
  opts: SessionStreamOptions,
): () => void {
  const path = `/api/sessions/${encodeURIComponent(sessionId)}/events`;
  return openStream<TypedEvent>({
    ...opts,
    url: (lastSeq) => (lastSeq > 0 ? `${path}?after=${lastSeq}` : path),
    eventName: "flow",
    parse: parseEvent,
    seqOf: (e) => (isDurable(e.kind) ? e.seq : undefined),
  });
}

export interface AppStreamOptions extends StreamOptions {
  onEvent: (event: AppEvent) => void;
  // Each (re)open: the stream has no replay, so reload the lists.
  onOpen?: () => void;
}

// streamAppEvents follows GET /api/events: SSE frames named "app", one
// AppEvent each, no seq.
export function streamAppEvents(opts: AppStreamOptions): () => void {
  return openStream<AppEvent>({
    ...opts,
    url: () => "/api/events",
    eventName: "app",
    parse: (data) => {
      const v: unknown = JSON.parse(data);
      return typeof v === "object" && v !== null && "kind" in v
        ? (v as AppEvent)
        : null;
    },
    seqOf: () => undefined,
  });
}
