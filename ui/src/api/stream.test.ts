import { FakeEventSource } from "../test/fakeEventSource";
import type { TypedEvent } from "../contract/events.gen";
import type { AppEvent } from "./types.gen";
import { streamAppEvents, streamSessionEvents } from ".";

function ev(seq: number | undefined, kind: string, data: unknown) {
  return {
    v: 1,
    ...(seq === undefined ? {} : { seq }),
    ts: "2026-11-26T12:00:00Z",
    session: "s1",
    kind,
    data,
  };
}

const status = (seq: number) =>
  ev(seq, "session.status", { status: "working" });

describe("streamSessionEvents", () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("delivers flow frames once, in seq order, ephemeral ones always", () => {
    const got: TypedEvent[] = [];
    streamSessionEvents("s1", {
      EventSource: FakeEventSource,
      onEvent: (e) => got.push(e),
    });
    const es = FakeEventSource.last();
    expect(es.url).toBe("/api/sessions/s1/events");
    es.open();
    es.emit("flow", status(1));
    es.emit("flow", status(2));
    es.emit("flow", status(2));
    es.emit("flow", status(1));
    es.emit(
      "flow",
      ev(undefined, "assistant.text_delta", { blockId: "b", text: "x" }),
    );
    es.emit(
      "flow",
      ev(undefined, "assistant.text_delta", { blockId: "b", text: "y" }),
    );
    es.emit("flow", status(3));
    // Not a flow frame, not JSON, an unknown kind: all skipped.
    es.emit("message", status(4));
    es.emit("flow", "{not json");
    es.emit("flow", ev(9, "future.kind", {}));
    expect(got.map((e) => e.seq ?? e.kind)).toEqual([
      1,
      2,
      "assistant.text_delta",
      "assistant.text_delta",
      3,
    ]);
  });

  it("leaves reconnects to the browser while it retries", () => {
    streamSessionEvents("s1", {
      EventSource: FakeEventSource,
      onEvent: () => {},
    });
    FakeEventSource.last().fail(false);
    vi.advanceTimersByTime(60000);
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it("recreates a closed stream after the last seq, with a growing backoff", () => {
    const got: number[] = [];
    const onOpen = vi.fn();
    streamSessionEvents("s1", {
      EventSource: FakeEventSource,
      backoffMs: 100,
      maxBackoffMs: 250,
      onEvent: (e) => got.push(e.seq ?? -1),
      onOpen,
    });
    const first = FakeEventSource.last();
    first.open();
    first.emit("flow", status(1));
    first.emit("flow", status(2));
    first.fail(true);
    expect(first.closed).toBe(true);
    vi.advanceTimersByTime(99);
    expect(FakeEventSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    const second = FakeEventSource.last();
    expect(second.url).toBe("/api/sessions/s1/events?after=2");
    // Late frames of the old source are ignored.
    first.emit("flow", status(7));
    second.emit("flow", status(2));
    second.emit("flow", status(3));
    expect(got).toEqual([1, 2, 3]);

    second.fail(true);
    vi.advanceTimersByTime(199);
    expect(FakeEventSource.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    const third = FakeEventSource.last();
    expect(third.url).toBe("/api/sessions/s1/events?after=3");
    third.fail(true);
    vi.advanceTimersByTime(249);
    expect(FakeEventSource.instances).toHaveLength(3);
    vi.advanceTimersByTime(1);
    // An open resets the backoff.
    const fourth = FakeEventSource.last();
    fourth.open();
    expect(onOpen).toHaveBeenCalledTimes(2);
    fourth.fail(true);
    vi.advanceTimersByTime(100);
    expect(FakeEventSource.instances).toHaveLength(5);
  });

  it("starts after a given seq", () => {
    const got: number[] = [];
    streamSessionEvents("s 1", {
      EventSource: FakeEventSource,
      after: 5,
      onEvent: (e) => got.push(e.seq ?? -1),
    });
    const es = FakeEventSource.last();
    expect(es.url).toBe("/api/sessions/s%201/events?after=5");
    es.emit("flow", status(5));
    es.emit("flow", status(6));
    expect(got).toEqual([6]);
  });

  it("closes for good: no events, no reconnect", () => {
    const onEvent = vi.fn();
    const close = streamSessionEvents("s1", {
      EventSource: FakeEventSource,
      backoffMs: 10,
      onEvent,
    });
    const es = FakeEventSource.last();
    es.fail(true);
    close();
    vi.advanceTimersByTime(1000);
    expect(FakeEventSource.instances).toHaveLength(1);
    es.emit("flow", status(1));
    expect(onEvent).not.toHaveBeenCalled();

    const close2 = streamSessionEvents("s2", {
      EventSource: FakeEventSource,
      onEvent,
    });
    const es2 = FakeEventSource.last();
    close2();
    expect(es2.closed).toBe(true);
    es2.open();
    es2.fail(true);
    vi.advanceTimersByTime(60000);
    expect(FakeEventSource.instances).toHaveLength(2);
  });

  it("does nothing without an EventSource", () => {
    const close = streamSessionEvents("s1", { onEvent: () => {} });
    expect(() => close()).not.toThrow();
  });
});

describe("streamAppEvents", () => {
  beforeEach(() => {
    FakeEventSource.reset();
  });

  it("passes app frames through and reports every open", () => {
    const got: AppEvent[] = [];
    const onOpen = vi.fn();
    streamAppEvents({
      EventSource: FakeEventSource,
      onEvent: (e) => got.push(e),
      onOpen,
    });
    const es = FakeEventSource.last();
    expect(es.url).toBe("/api/events");
    es.open();
    es.emit("app", { kind: "session.removed", sessionId: "a" });
    es.emit("app", { kind: "session.removed", sessionId: "a" });
    es.emit("app", "null");
    es.emit("app", "[]");
    expect(got).toHaveLength(2);
    expect(onOpen).toHaveBeenCalledTimes(1);
  });
});
