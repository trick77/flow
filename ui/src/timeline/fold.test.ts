import type { TypedEvent } from "../contract/events.gen";
import { fixtureEvents, mk } from "../test/flowEvents";
import {
  foldEvents,
  type FoldItem,
  type GateRow,
  type PermissionItem,
  type ToolRow,
} from "./fold";
import { emptyStream, reduceStream, type StreamState } from "./state";

function stateOf(events: TypedEvent[]): StreamState {
  return events.reduce<StreamState>(
    (s, event) => reduceStream(s, { type: "event", event }),
    emptyStream,
  );
}

// Fold raw events (no seq dedupe), as a log with gaps would come in.
function foldRaw(events: TypedEvent[]) {
  return foldEvents({ ...emptyStream, events });
}

describe("foldEvents on the demo turn", () => {
  const demo = fixtureEvents("demo.jsonl");
  const folded = foldEvents(stateOf(demo));

  it("lays out message, fold, todo, permission, answer, files", () => {
    expect(folded.items.map((i) => i.type)).toEqual([
      "user",
      "fold",
      "todo",
      "permission",
      "text",
      "files",
    ]);
    expect(folded.items.map((i) => i.key)).toEqual([
      "msg:0193a8f2-9100-7000-8000-000000000001:3",
      "turn:t-1",
      "todo:t-1",
      "perm:perm-d1",
      "text:msg_d9:0",
      "files:26",
    ]);
    expect(folded.status).toBe("done");
    expect(folded.turnOpen).toBe(false);
    expect(folded.openTool).toBeUndefined();
    expect(folded.provider).toBe("claude");
  });

  it("puts every tool and the gate into the turn's fold", () => {
    const fold = folded.items[1] as FoldItem;
    expect(fold.running).toBe(false);
    expect(fold.durationMs).toBe(221000);
    expect(fold.outcome).toBe("completed");
    expect(fold.startedAt).toBe("2026-11-26T12:49:01.01Z");
    expect(fold.rows.map((r) => r.key)).toEqual([
      "tool:toolu_d1",
      "tool:toolu_d2",
      "tool:toolu_d3",
      "tool:toolu_d4",
      "tool:toolu_d5",
      "tool:toolu_d6",
      "gate:22",
    ]);
    const search = fold.rows[1] as ToolRow;
    expect(search).toMatchObject({
      kind: "search",
      pattern: "--ui-sidebar-pref",
      path: "scener/ui/src",
      finished: { status: "ok", result: { matches: 9 } },
    });
    const gate = fold.rows[6] as GateRow;
    expect(gate).toMatchObject({
      state: "finished",
      repo: "dev/scener",
      exitCode: 0,
      durationMs: 61000,
    });
  });

  it("resolves the permission request", () => {
    const perm = folded.items[3] as PermissionItem;
    expect(perm.request.command).toBe("make check");
    expect(perm.decision).toBe("allow_once");
  });

  it("shows the turn as running, with its open tool, part way in", () => {
    const part = foldEvents(stateOf(demo.slice(0, 21)));
    expect(part.turnOpen).toBe(true);
    expect(part.status).toBe("working");
    expect(part.openTool?.toolId).toBe("toolu_d6");
    expect((part.items[1] as FoldItem).running).toBe(true);
    const asking = foldEvents(stateOf(demo.slice(0, 18)));
    expect(asking.status).toBe("approval");
    expect((asking.items[3] as PermissionItem).decision).toBeUndefined();
    expect(asking.openTool).toBeUndefined();
  });

  it("keeps keys stable as events arrive", () => {
    const a = foldEvents(stateOf(demo.slice(0, 15)));
    const b = foldEvents(stateOf(demo));
    expect(a.items.map((i) => i.key)).toEqual(
      b.items.slice(0, a.items.length).map((i) => i.key),
    );
  });
});

describe("foldEvents on the kind fixtures", () => {
  // Not a stream: seqs restart, turns have no turn.started. It must still
  // fold without throwing.
  const folded = foldRaw(fixtureEvents("events.jsonl"));

  it("folds every kind without throwing", () => {
    const types = new Set(folded.items.map((i) => i.type));
    for (const t of [
      "user",
      "fold",
      "text",
      "todo",
      "permission",
      "files",
      "resumed",
      "notice",
    ]) {
      expect(types, t).toContain(t);
    }
  });

  it("nests subagent tools and pairs queued, started and finished gates", () => {
    const fold = folded.items.find((i) => i.key === "turn:t-0001") as FoldItem;
    const nested = fold.rows.find((r) => r.key === "tool:toolu_06") as ToolRow;
    expect(nested.nested).toBe(true);
    const gates = fold.rows.filter((r) => r.type === "gate");
    expect(gates).toHaveLength(1);
    expect(gates[0]).toMatchObject({
      state: "finished",
      position: 2,
      aheadRepo: "dev/toolkit",
      exitCode: 0,
    });
    const bash = fold.rows.find((r) => r.key === "tool:toolu_03") as ToolRow;
    expect(bash.finished?.status).toBe("error");
  });

  it("shows the notice with its params", () => {
    const n = folded.items.find((i) => i.type === "notice");
    expect(n).toMatchObject({
      code: "context_compacted",
      level: "info",
      params: { trigger: "auto" },
    });
  });
});

describe("foldEvents edge cases", () => {
  it("appends open blocks after the log, without finished ones", () => {
    const s: StreamState = {
      ...stateOf([
        mk("assistant.text", { blockId: "b1", text: "a" }, { seq: 1 }),
      ]),
      live: [
        { blockId: "b1", text: "stale" },
        { blockId: "b2", text: "typing" },
      ],
    };
    const f = foldEvents(s);
    expect(f.items).toEqual([
      { type: "text", key: "text:b1", text: "a", live: false },
      { type: "text", key: "text:b2", text: "typing", live: true },
    ]);
  });

  it("merges repeated blocks, tools, requests and todos", () => {
    const f = foldRaw([
      mk("assistant.text", { blockId: "b", text: "one" }, { seq: 1 }),
      mk("assistant.text", { blockId: "b", text: "two" }, { seq: 2 }),
      mk(
        "tool.started",
        { toolId: "t", name: "Read", kind: "read" },
        { seq: 3, turn: "x" },
      ),
      mk(
        "tool.started",
        { toolId: "t", name: "Read", kind: "read" },
        { seq: 4, turn: "x" },
      ),
      mk("tool.finished", { toolId: "nope", status: "ok" }, { seq: 5 }),
      mk(
        "permission.requested",
        {
          requestId: "r",
          tool: "Bash",
          kind: "command",
          mode: "ask",
          canAlwaysAllow: false,
        },
        { seq: 6 },
      ),
      mk(
        "permission.requested",
        {
          requestId: "r",
          tool: "Bash",
          kind: "command",
          mode: "ask",
          canAlwaysAllow: false,
        },
        { seq: 7 },
      ),
      mk(
        "permission.resolved",
        { requestId: "other", decision: "deny" },
        { seq: 8 },
      ),
      mk(
        "todo.updated",
        { items: [{ text: "a", status: "pending" }] },
        { seq: 9 },
      ),
      mk(
        "todo.updated",
        { items: [{ text: "b", status: "completed" }] },
        { seq: 10 },
      ),
    ]);
    expect(f.items.map((i) => i.key)).toEqual([
      "text:b",
      "turn:x",
      "perm:r",
      "todo:9",
    ]);
    expect(f.items[0]).toMatchObject({ text: "two" });
    expect((f.items[1] as FoldItem).rows).toHaveLength(1);
    expect(f.items[3]).toMatchObject({ items: [{ text: "b" }] });
  });

  it("groups turnless activity and starts gates without a queue entry", () => {
    const f = foldRaw([
      mk(
        "tool.started",
        { toolId: "a", name: "Bash", kind: "bash" },
        { seq: 1 },
      ),
      mk("gate.started", {}, { seq: 2 }),
      mk("gate.finished", { exitCode: 2, durationMs: 5 }, { seq: 3 }),
      mk(
        "gate.finished",
        { repo: "r", exitCode: 0, durationMs: 1 },
        { seq: 4 },
      ),
      mk(
        "message.user",
        { messageId: "m", text: "hi", origin: "user" },
        { seq: 5 },
      ),
      mk("gate.queued", { position: 1 }, { seq: 6 }),
    ]);
    expect(f.items.map((i) => i.key)).toEqual(["fold:1", "msg:m:5", "fold:6"]);
    const rows = (f.items[0] as FoldItem).rows;
    expect(rows.map((r) => r.key)).toEqual(["tool:a", "gate:2", "gate:4"]);
    expect(rows[1]).toMatchObject({ state: "finished", exitCode: 2 });
    expect((f.items[2] as FoldItem).rows[0]).toMatchObject({
      state: "queued",
      position: 1,
    });
  });
});
