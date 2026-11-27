import { fixtureEvents, mk } from "../test/flowEvents";
import { emptyStream, reduceStream, type StreamState } from "./state";

function feed(events: Parameters<typeof mk>[]): StreamState {
  return events.reduce<StreamState>(
    (s, args) => reduceStream(s, { type: "event", event: mk(...args) }),
    emptyStream,
  );
}

describe("reduceStream", () => {
  it("keeps durable events in seq order and drops replays", () => {
    let s = emptyStream;
    for (const e of fixtureEvents("demo.jsonl")) {
      s = reduceStream(s, { type: "event", event: e });
    }
    const replay = reduceStream(s, {
      type: "event",
      event: fixtureEvents("demo.jsonl")[3],
    });
    expect(replay).toBe(s);
    expect(s.lastSeq).toBe(28);
    expect(s.events.map((e) => e.seq)).toEqual(
      Array.from({ length: 28 }, (_, i) => i + 1),
    );
  });

  it("appends deltas to an open block until the durable text replaces it", () => {
    let s = feed([
      ["assistant.text_delta", { blockId: "b1", text: "Hel" }],
      ["assistant.text_delta", { blockId: "b2", text: "Other" }],
      ["assistant.text_delta", { blockId: "b1", text: "lo" }],
    ]);
    expect(s.live).toEqual([
      { blockId: "b1", text: "Hello" },
      { blockId: "b2", text: "Other" },
    ]);
    s = reduceStream(s, {
      type: "event",
      event: mk(
        "assistant.text",
        { blockId: "b1", text: "Hello." },
        { seq: 1 },
      ),
    });
    expect(s.live).toEqual([{ blockId: "b2", text: "Other" }]);
    // A late delta of a finished block is stale.
    const late = reduceStream(s, {
      type: "event",
      event: mk("assistant.text_delta", { blockId: "b1", text: "!" }),
    });
    expect(late).toBe(s);
  });

  it("drops half-streamed blocks when their turn ends without their text", () => {
    let s = feed([
      [
        "turn.started",
        { messageId: "m1", provider: "claude", model: "x" },
        { seq: 1, turn: "t1" },
      ],
      [
        "assistant.text_delta",
        { blockId: "b1", text: "Half a para" },
        { turn: "t1" },
      ],
    ]);
    expect(s.live).toEqual([{ blockId: "b1", text: "Half a para" }]);
    s = reduceStream(s, {
      type: "event",
      event: mk(
        "turn.ended",
        { outcome: "lost", durationMs: 0 },
        { seq: 2, turn: "t1" },
      ),
    });
    expect(s.live).toEqual([]);
    // A late delta of the ended turn's block is stale.
    const late = reduceStream(s, {
      type: "event",
      event: mk("assistant.text_delta", { blockId: "b1", text: "graph" }),
    });
    expect(late).toBe(s);
  });

  it("takes a snapshot's blocks as the whole set, without finished ones", () => {
    let s = feed([
      ["assistant.text", { blockId: "done", text: "x" }, { seq: 1 }],
      ["assistant.text_delta", { blockId: "old", text: "gone" }],
    ]);
    s = reduceStream(s, {
      type: "event",
      event: mk("live.snapshot", {
        blocks: [
          { blockId: "done", text: "x" },
          { blockId: "b3", text: "So far" },
        ],
      }),
    });
    expect(s.live).toEqual([{ blockId: "b3", text: "So far" }]);
  });

  it("clears open blocks on open and ignores tool progress", () => {
    const s = feed([["assistant.text_delta", { blockId: "b", text: "x" }]]);
    const opened = reduceStream(s, { type: "open" });
    expect(opened.live).toEqual([]);
    expect(reduceStream(opened, { type: "open" })).toBe(opened);
    const progress = reduceStream(opened, {
      type: "event",
      event: mk("tool.progress", { toolId: "t", elapsedMs: 5 }),
    });
    expect(progress).toBe(opened);
  });

  it("numbers a durable event without seq after the last one", () => {
    const s = feed([["notice", { code: "c", level: "info" }]]);
    expect(s.lastSeq).toBe(1);
  });
});
