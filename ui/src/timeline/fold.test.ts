import type { TypedEvent } from "../contract/events.gen";
import { fixtureEvents, mk } from "../test/flowEvents";
import {
  foldEvents,
  type FoldItem,
  type GateRow,
  type PermissionItem,
  type PlanItem,
  type QuestionItem,
  THINKING_TOKENS as THINKING,
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
    expect(perm.remembered).toBeUndefined();
  });

  it("marks a request the host allowed from the session's earlier choice", () => {
    const f = foldRaw([
      mk(
        "permission.requested",
        {
          requestId: "r",
          tool: "Bash",
          kind: "command",
          command: "echo hi",
          mode: "ask",
          canAlwaysAllow: false,
        },
        { seq: 1 },
      ),
      mk(
        "permission.resolved",
        { requestId: "r", decision: "allow_always", remembered: true },
        { seq: 2 },
      ),
    ]);
    expect(f.items[0]).toMatchObject({
      decision: "allow_always",
      remembered: true,
    });
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
      "question",
      "plan",
      "files",
      "resumed",
      "notice",
    ]) {
      expect(types, t).toContain(t);
    }
  });

  it("resolves the fixture's question and plan", () => {
    const q = folded.items.find((i) => i.type === "question") as QuestionItem;
    expect(q.open).toBe(false);
    expect(q.resolved?.answers?.[0].optionIds).toEqual(["o0"]);
    const p = folded.items.find((i) => i.type === "plan") as PlanItem;
    expect(p.resolved?.decision).toBe("approved");
    expect(folded.pendingQuestion).toBeUndefined();
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
  it("shows no row for the provider_session marker, but other notices", () => {
    const f = foldEvents(
      stateOf([
        mk("notice", { code: "provider_session", level: "info" }, { seq: 1 }),
        mk(
          "notice",
          { code: "message_may_not_have_run", level: "error" },
          { seq: 2 },
        ),
      ]),
    );
    expect(f.items.map((i) => i.type === "notice" && i.code)).toEqual([
      "message_may_not_have_run",
    ]);
  });

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

describe("foldEvents: questions, plans, the queue", () => {
  const question = {
    requestId: "q1",
    questions: [
      {
        id: "a",
        text: "Which key?",
        multiSelect: false,
        options: [{ id: "o1", label: "SHA" }],
      },
    ],
  };
  const plan = { requestId: "p1", plan: "1. Do it" };
  const turn = (id: string, seq: number) =>
    mk("turn.started", { messageId: `m-${id}` }, { seq, turn: id });
  const end = (id: string, seq: number) =>
    mk(
      "turn.ended",
      { outcome: "interrupted", durationMs: 5 },
      { seq, turn: id },
    );

  it("keeps an unanswered question of the running turn pending", () => {
    const f = foldRaw([
      turn("t1", 1),
      mk("question.asked", question, { seq: 2, turn: "t1" }),
      mk("question.asked", question, { seq: 3, turn: "t1" }),
      mk("plan.proposed", plan, { seq: 4, turn: "t1" }),
    ]);
    const q = f.items.filter((i) => i.type === "question");
    expect(q).toHaveLength(1);
    expect(q[0]).toMatchObject({ key: "question:q1", open: true });
    expect(f.pendingQuestion?.request.requestId).toBe("q1");
    expect(f.items.find((i) => i.type === "plan")).toMatchObject({
      key: "plan:p1",
      open: true,
    });
  });

  it("closes requests that were resolved or whose turn ended", () => {
    const answered = foldRaw([
      turn("t1", 1),
      mk("question.asked", question, { seq: 2, turn: "t1" }),
      mk(
        "question.resolved",
        { requestId: "q1", outcome: "answered", text: "tree hash" },
        { seq: 3, turn: "t1" },
      ),
      mk("plan.proposed", plan, { seq: 4, turn: "t1" }),
      mk(
        "plan.resolved",
        { requestId: "p1", decision: "rejected", feedback: "smaller" },
        { seq: 5, turn: "t1" },
      ),
      mk("question.resolved", { requestId: "gone", outcome: "expired" }),
      mk("plan.resolved", { requestId: "gone", decision: "expired" }),
    ]);
    expect(answered.pendingQuestion).toBeUndefined();
    expect(answered.items.find((i) => i.type === "question")).toMatchObject({
      open: false,
      resolved: { text: "tree hash" },
    });
    expect(answered.items.find((i) => i.type === "plan")).toMatchObject({
      open: false,
      resolved: { decision: "rejected" },
    });

    const cut = foldRaw([
      turn("t1", 1),
      mk("question.asked", question, { seq: 2, turn: "t1" }),
      mk("plan.proposed", plan, { seq: 3, turn: "t1" }),
      end("t1", 4),
    ]);
    expect(cut.pendingQuestion).toBeUndefined();
    const cutItems = cut.items.filter(
      (i) => i.type === "question" || i.type === "plan",
    );
    expect(cutItems.map((i) => [i.type, i.open, i.resolved])).toEqual([
      ["question", false, undefined],
      ["plan", false, undefined],
    ]);
  });

  it("keeps the queue in order, front first, each id once", () => {
    const q = (id: string, seq: number, front?: boolean) =>
      mk(
        "message.queued",
        { messageId: id, text: `text ${id}`, origin: "user", front },
        { seq },
      );
    const f = foldRaw([
      q("a", 1),
      q("b", 2),
      q("c", 3),
      // Sent now: logged again with front, it moves to the head.
      q("c", 4, true),
      mk("message.dequeued", { messageId: "a", reason: "removed" }, { seq: 5 }),
    ]);
    expect(f.queue.map((m) => m.messageId)).toEqual(["c", "b"]);
    expect(f.queue[0]).toEqual({
      messageId: "c",
      text: "text c",
      origin: "user",
    });

    const delivered = foldRaw([
      q("a", 1),
      mk(
        "message.user",
        { messageId: "a", text: "text a", origin: "user" },
        { seq: 2 },
      ),
    ]);
    expect(delivered.queue).toEqual([]);
  });
});

describe("provider messages the timeline does not show", () => {
  const t = "t-1";
  const pt = (name: string, raw: unknown, seq: number, rawTruncated = false) =>
    mk(
      "provider.passthrough",
      { provider: "claude", name, raw, ...(rawTruncated && { rawTruncated }) },
      { seq, turn: t },
    );
  const started = mk("turn.started", { messageId: "m" }, { seq: 1, turn: t });
  const rows = (f: ReturnType<typeof foldRaw>) =>
    f.items.flatMap((i) => (i.type === "fold" ? i.rows : []));

  it("hides CLI bookkeeping and the interrupt marker, keeps everything else", () => {
    const f = foldRaw([
      started,
      pt("command_lifecycle", { type: "command_lifecycle", state: "done" }, 2),
      pt("rate_limit_event", { type: "rate_limit_event" }, 3),
      pt(
        "user",
        {
          type: "user",
          message: {
            role: "user",
            content: [{ type: "text", text: "[Request interrupted by user]" }],
          },
        },
        4,
      ),
      pt(
        "user",
        {
          type: "user",
          message: {
            role: "user",
            content: "[Request interrupted by user for tool use]",
          },
        },
        5,
      ),
      // A user message with more than the marker, a truncated one, an
      // unknown kind: still rows.
      pt(
        "user",
        {
          type: "user",
          message: {
            role: "user",
            content: [
              { type: "text", text: "[Request interrupted by user]" },
              { type: "text", text: "and something else" },
            ],
          },
        },
        6,
      ),
      pt(
        "user",
        '{"type":"user","message":{"role":"user","content":"[Req',
        7,
        true,
      ),
      pt("system/workspace_indexed", { type: "system" }, 8),
    ]);
    expect(rows(f).map((r) => r.key)).toEqual(["raw:6", "raw:7", "raw:8"]);
  });

  it("makes no empty fold for a hidden message outside a turn", () => {
    const f = foldRaw([
      mk(
        "provider.passthrough",
        { provider: "claude", name: "command_lifecycle", raw: {} },
        { seq: 1 },
      ),
    ]);
    expect(f.items).toEqual([]);
  });

  it("shows the same names of another provider as rows", () => {
    const f = foldRaw([
      started,
      mk(
        "provider.passthrough",
        { provider: "opencode", name: "rate_limit_event", raw: {} },
        { seq: 2, turn: t },
      ),
    ]);
    expect(rows(f).map((r) => r.key)).toEqual(["raw:2"]);
  });

  it("turns thinking estimates into one indicator for the running turn", () => {
    const thinking = [
      started,
      pt(THINKING, { estimated_tokens: 120, estimated_tokens_delta: 120 }, 2),
      pt(THINKING, { estimated_tokens: 1834, estimated_tokens_delta: 1714 }, 3),
    ];
    const f = foldRaw(thinking);
    expect(rows(f)).toEqual([]);
    expect(f.thinking).toEqual({ tokens: 1834 });

    // Over once the agent does something else, or the turn ends.
    const tool = mk(
      "tool.started",
      { toolId: "tl", name: "Read", kind: "read", path: "a.go" },
      { seq: 4, turn: t },
    );
    expect(foldRaw([...thinking, tool]).thinking).toBeUndefined();
    const ended = mk(
      "turn.ended",
      { outcome: "completed", durationMs: 1 },
      { seq: 4, turn: t },
    );
    expect(foldRaw([...thinking, ended]).thinking).toBeUndefined();
    // A hidden message in between does not end it.
    expect(
      foldRaw([...thinking, pt("command_lifecycle", {}, 4)]).thinking,
    ).toEqual({ tokens: 1834 });
    // Without a readable estimate: the indicator alone.
    expect(foldRaw([started, pt(THINKING, { x: 1 }, 2)]).thinking).toEqual({});
    // Not while the answer streams already.
    expect(
      foldEvents({
        ...emptyStream,
        events: thinking,
        live: [{ blockId: "b", text: "Here" }],
      }).thinking,
    ).toBeUndefined();
  });
});
