// Mapping tests: SDK messages and callbacks in, Flow drafts out. Every
// mapping here rests on the SDK's documentation and its type definitions;
// a test named "[docs only]" still needs a recorded real session to confirm
// the SDK actually sends that shape (contract tests replace the synthetic
// fixtures with recordings).

import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type {
  SDKAssistantMessage,
  SDKMessage,
  SDKPartialAssistantMessage,
  SDKResultMessage,
  SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk";
import { describe, expect, it } from "vitest";

import type { EventDraft, PermissionMode } from "./contract/events.gen.js";
import { CAP, LINE_BUDGET, byteLength, fitDraft, goSize } from "./limits.js";
import {
  permissionResult,
  planResult,
  planText,
  questionResult,
  requestDraft,
  type ToolRequest,
} from "./requests.js";
import { draftErrors, fixtureLines } from "./testing/schema.js";
import { toolKind } from "./tools.js";
import { Translator, type Turn } from "./translate.js";

const CWD = "/home/flow/work/.sessions/flaky";
const SID = "0193a8f2-5d1e-7c3a-9b2e-6f1d2a3b4c5d";
const U = "00000000-0000-4000-8000-000000000000" as SDKUserMessage["uuid"] &
  string;

function translator(
  opts: { now?: () => number; mode?: PermissionMode; prior?: number } = {},
) {
  return new Translator({
    cwd: CWD,
    claudeAuth: "subscription_token",
    mode: () => opts.mode ?? "ask",
    priorCostUsd: opts.prior ?? 0,
    now: opts.now ?? (() => 0),
  });
}

function turn(over: Partial<Turn> = {}): Turn {
  return { uuid: U, interrupted: false, denied: new Set(), ...over };
}

function valid(drafts: EventDraft[]): EventDraft[] {
  for (const d of drafts)
    expect(draftErrors(d), JSON.stringify(d).slice(0, 300)).toEqual([]);
  return drafts;
}

function assistant(
  id: string,
  content: unknown[],
  parent: string | null = null,
): SDKAssistantMessage {
  return {
    type: "assistant",
    uuid: U,
    session_id: SID,
    parent_tool_use_id: parent,
    message: {
      id,
      role: "assistant",
      content,
    } as unknown as SDKAssistantMessage["message"],
  };
}

function toolResult(
  toolId: string,
  content: unknown,
  opts: { isError?: boolean; structured?: unknown; parent?: string } = {},
): SDKUserMessage {
  const m: SDKUserMessage = {
    type: "user",
    uuid: U,
    session_id: SID,
    parent_tool_use_id: opts.parent ?? null,
    message: {
      role: "user",
      content: [
        {
          type: "tool_result",
          tool_use_id: toolId,
          content,
          is_error: opts.isError ?? false,
        } as never,
      ],
    },
  };
  if (opts.structured !== undefined) m.tool_use_result = opts.structured;
  return m;
}

function stream(event: unknown): SDKPartialAssistantMessage {
  return {
    type: "stream_event",
    uuid: U,
    session_id: SID,
    parent_tool_use_id: null,
    event: event as SDKPartialAssistantMessage["event"],
  };
}

function result(over: Record<string, unknown> = {}): SDKResultMessage {
  return {
    type: "result",
    subtype: "success",
    uuid: U,
    session_id: SID,
    duration_ms: 1000,
    duration_api_ms: 800,
    is_error: false,
    num_turns: 1,
    result: "Done.",
    stop_reason: "end_turn",
    total_cost_usd: 0.1,
    usage: { input_tokens: 10, output_tokens: 5 },
    modelUsage: {},
    permission_denials: [],
    ...over,
  } as unknown as SDKResultMessage;
}

interface MappingCase {
  case: string;
  in: Record<string, unknown>;
  out: EventDraft[];
}

describe("contract/fixtures/mapping/claude.jsonl", () => {
  for (const c of fixtureLines<MappingCase>("mapping/claude.jsonl")) {
    it(`[docs only] ${c.case}`, () => {
      let drafts: EventDraft[];
      if (c.in.callback === "canUseTool") {
        const o = c.in.options as Record<string, unknown>;
        const req: ToolRequest = {
          requestId: String(o.requestId),
          toolName: String(c.in.toolName),
          input: c.in.input as Record<string, unknown>,
          toolUseID: String(o.toolUseID),
          ...(o.suggestions !== undefined && {
            suggestions: o.suggestions as never,
          }),
        };
        drafts = [requestDraft(req, { cwd: CWD, mode: "ask" })];
      } else {
        const tr = translator();
        // The fixtures are single messages: the state a stream would have
        // built is seeded (the open stream message, an interrupt).
        if (c.in.type === "stream_event") tr.seedStreamMessage("msg_01");
        const t = turn({ interrupted: c.case.includes("interrupted") });
        drafts = tr.translate(c.in as unknown as SDKMessage, t).drafts;
      }
      expect(valid(drafts)).toEqual(c.out);
    });
  }
});

describe("assistant text and thinking", () => {
  it("[docs only] deltas and the durable block share message id : stream index", () => {
    const tr = translator();
    const out = [
      ...tr.translate(
        stream({ type: "message_start", message: { id: "msg_A" } }),
      ).drafts,
      ...tr.translate(
        stream({
          type: "content_block_start",
          index: 0,
          content_block: { type: "thinking" },
        }),
      ).drafts,
      ...tr.translate(
        stream({
          type: "content_block_delta",
          index: 0,
          delta: { type: "thinking_delta", thinking: "hm" },
        }),
      ).drafts,
      ...tr.translate(
        assistant("msg_A", [
          { type: "thinking", thinking: "hm", signature: "s" },
        ]),
      ).drafts,
      ...tr.translate(
        stream({
          type: "content_block_delta",
          index: 1,
          delta: { type: "text_delta", text: "Hel" },
        }),
      ).drafts,
      ...tr.translate(
        stream({
          type: "content_block_delta",
          index: 1,
          delta: { type: "text_delta", text: "lo" },
        }),
      ).drafts,
      ...tr.translate(assistant("msg_A", [{ type: "text", text: "Hello" }]))
        .drafts,
      ...tr.translate(stream({ type: "message_stop" })).drafts,
    ];
    expect(valid(out)).toEqual([
      { kind: "assistant.thinking", data: { blockId: "msg_A:0", text: "hm" } },
      {
        kind: "assistant.text_delta",
        data: { blockId: "msg_A:1", text: "Hel" },
      },
      {
        kind: "assistant.text_delta",
        data: { blockId: "msg_A:1", text: "lo" },
      },
      { kind: "assistant.text", data: { blockId: "msg_A:1", text: "Hello" } },
    ]);
  });

  it("[docs only] an unknown stream event and an unknown block pass through", () => {
    const tr = translator();
    const out = valid([
      ...tr.translate(stream({ type: "content_block_hologram", index: 0 }))
        .drafts,
      ...tr.translate(
        assistant("msg_B", [{ type: "redacted_thinking", data: "x" }]),
      ).drafts,
    ]);
    expect(out.map((d) => (d.data as { name: string }).name)).toEqual([
      "stream_event/content_block_hologram",
      "assistant/redacted_thinking",
    ]);
  });

  it("[docs only] an assistant error is a notice and the turn's error code", () => {
    const tr = translator();
    const t = turn();
    const m = {
      ...assistant("msg_E", [{ type: "text", text: "API Error" }]),
      error: "rate_limit" as const,
    };
    const out = valid(tr.translate(m, t).drafts);
    expect(out[1]).toEqual({
      kind: "notice",
      data: {
        code: "rate_limited",
        level: "warning",
        params: { error: "rate_limit" },
      },
    });
    const end = valid(tr.translate(result({ is_error: true }), t).drafts);
    expect(end.at(-1)?.data).toMatchObject({
      outcome: "error",
      errorCode: "rate_limit",
    });
  });
});

describe("tools", () => {
  it("maps tool names to kinds", () => {
    const want: Record<string, string> = {
      Read: "read",
      Edit: "edit",
      MultiEdit: "edit",
      Write: "write",
      Grep: "search",
      Glob: "search",
      Bash: "bash",
      WebFetch: "web",
      WebSearch: "web",
      TodoWrite: "todo",
      TaskCreate: "todo",
      TaskUpdate: "todo",
      Agent: "subagent",
      Task: "subagent",
      AskUserQuestion: "question",
      ExitPlanMode: "plan",
      mcp__flow__create_session: "mcp",
      Workbench: "other",
    };
    for (const [name, kind] of Object.entries(want))
      expect(toolKind(name), name).toBe(kind);
  });

  it("[docs only] times a call from tool_use to tool_result", () => {
    let now = 1000;
    const tr = translator({ now: () => now });
    const start = valid(
      tr.translate(
        assistant("msg_T", [
          {
            type: "tool_use",
            id: "toolu_B1",
            name: "Bash",
            input: { command: "make check", description: "Run the gate" },
          },
        ]),
      ).drafts,
    );
    expect(start[0].data).toEqual({
      toolId: "toolu_B1",
      name: "Bash",
      kind: "bash",
      command: "make check",
      description: "Run the gate",
      input: { command: "make check", description: "Run the gate" },
    });
    now = 3500;
    const done = valid(
      tr.translate(
        toolResult("toolu_B1", "ok", {
          structured: { stdout: "ok", stderr: "", interrupted: false },
        }),
        turn(),
      ).drafts,
    );
    expect(done).toEqual([
      {
        kind: "tool.finished",
        data: {
          toolId: "toolu_B1",
          status: "ok",
          output: "ok",
          durationMs: 2500,
        },
      },
    ]);
  });

  it("[docs only] result status: error, denied, interrupted", () => {
    const tr = translator();
    const t = turn({ denied: new Set(["toolu_D"]) });
    expect(
      tr.translate(toolResult("toolu_X", "boom", { isError: true }), t)
        .drafts[0].data,
    ).toMatchObject({
      status: "error",
    });
    expect(
      tr.translate(toolResult("toolu_D", "denied", { isError: true }), t)
        .drafts[0].data,
    ).toMatchObject({
      status: "denied",
    });
    expect(
      tr.translate(
        toolResult("toolu_I", "x", { isError: true }),
        turn({ interrupted: true }),
      ).drafts[0].data,
    ).toMatchObject({ status: "interrupted" });
  });

  it("[docs only] edit, write and search facts from the structured output", () => {
    const tr = translator();
    tr.translate(
      assistant("msg_F", [
        {
          type: "tool_use",
          id: "toolu_E",
          name: "Edit",
          input: { file_path: `${CWD}/a.go` },
        },
        {
          type: "tool_use",
          id: "toolu_W",
          name: "Write",
          input: { file_path: "/tmp/b.go" },
        },
        {
          type: "tool_use",
          id: "toolu_G",
          name: "Grep",
          input: { pattern: "x" },
        },
      ]),
    );
    const patch = [
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 3,
        lines: [" a", "-b", "+c", "+d"],
      },
    ];
    const facts = (id: string, structured: unknown) =>
      valid(
        tr.translate(toolResult(id, "done", { structured }), turn()).drafts,
      )[0].data as {
        result?: unknown;
        rawResult?: unknown;
      };
    expect(facts("toolu_E", { structuredPatch: patch }).result).toEqual({
      added: 2,
      removed: 1,
    });
    expect(
      facts("toolu_W", {
        type: "create",
        content: "x\ny\n",
        structuredPatch: [],
      }).result,
    ).toEqual({
      created: true,
      added: 3,
      removed: 0,
    });
    const grep = facts("toolu_G", {
      numFiles: 2,
      numMatches: 7,
      filenames: [],
    });
    expect(grep.result).toEqual({ matches: 7 });
    expect(grep.rawResult).toBeUndefined();
  });

  it("[docs only] an MCP tool's structured result travels as rawResult", () => {
    const tr = translator();
    tr.translate(
      assistant("msg_M", [
        {
          type: "tool_use",
          id: "toolu_M",
          name: "mcp__flow__list_sessions",
          input: {},
        },
      ]),
    );
    const d = valid(
      tr.translate(
        toolResult("toolu_M", [{ type: "text", text: "2" }], {
          structured: { n: 2 },
        }),
        turn(),
      ).drafts,
    );
    expect(d[0].data).toMatchObject({
      status: "ok",
      output: "2",
      rawResult: { n: 2 },
    });
  });

  it("[docs only] a server tool and its result inside the assistant message", () => {
    const tr = translator();
    const out = valid(
      tr.translate(
        assistant("msg_S", [
          {
            type: "server_tool_use",
            id: "srvtoolu_1",
            name: "web_search",
            input: { query: "incus" },
          },
          {
            type: "web_search_tool_result",
            tool_use_id: "srvtoolu_1",
            content: [{ type: "web_search_result", url: "u" }],
          },
        ]),
        turn(),
      ).drafts,
    );
    expect(out.map((d) => d.kind)).toEqual(["tool.started", "tool.finished"]);
    expect(out[0].data).toMatchObject({ kind: "web" });
    expect(out[1].data).toMatchObject({
      status: "ok",
      rawResult: [{ type: "web_search_result", url: "u" }],
    });
  });

  it("[docs only] the TaskCreate/TaskUpdate generation keeps the todo list", () => {
    const tr = translator();
    tr.translate(
      assistant("msg_K", [
        {
          type: "tool_use",
          id: "toolu_C",
          name: "TaskCreate",
          input: {
            subject: "Run make check",
            description: "d",
            activeForm: "Running make check",
          },
        },
      ]),
    );
    const created = valid(
      tr.translate(
        toolResult("toolu_C", "ok", {
          structured: { task: { id: "1", subject: "Run make check" } },
        }),
        turn(),
      ).drafts,
    );
    expect(created[1]).toEqual({
      kind: "todo.updated",
      data: {
        items: [
          {
            id: "1",
            text: "Run make check",
            activeText: "Running make check",
            status: "pending",
          },
        ],
      },
    });
    tr.translate(
      assistant("msg_K2", [
        {
          type: "tool_use",
          id: "toolu_U",
          name: "TaskUpdate",
          input: { taskId: "1", status: "in_progress" },
        },
      ]),
    );
    const updated = valid(
      tr.translate(
        toolResult("toolu_U", "ok", {
          structured: { success: true, taskId: "1", updatedFields: ["status"] },
        }),
        turn(),
      ).drafts,
    );
    expect(updated[1].data).toEqual({
      items: [
        {
          id: "1",
          text: "Run make check",
          activeText: "Running make check",
          status: "in_progress",
        },
      ],
    });
  });

  it("closes tools still open at the turn's end as interrupted", () => {
    const tr = translator();
    tr.translate(
      assistant("msg_O", [
        {
          type: "tool_use",
          id: "toolu_O",
          name: "Read",
          input: { file_path: "/x" },
        },
      ]),
    );
    const out = valid(
      tr.translate(
        result({ subtype: "error_during_execution", errors: [] }),
        turn({ interrupted: true }),
      ).drafts,
    );
    expect(out.map((d) => d.kind)).toEqual(["tool.finished", "turn.ended"]);
    expect(out[0].data).toMatchObject({
      toolId: "toolu_O",
      status: "interrupted",
    });
    expect(out[1].data).toMatchObject({ outcome: "interrupted" });
  });
});

describe("subagents", () => {
  it("[docs only] the Agent call, its nested tools, progress and task bookends", () => {
    const tr = translator();
    const out = valid([
      ...tr.translate(
        assistant("msg_P", [
          {
            type: "tool_use",
            id: "toolu_A",
            name: "Agent",
            input: {
              description: "Find the schema",
              prompt: "p",
              subagent_type: "Explore",
            },
          },
        ]),
      ).drafts,
      ...tr.translate({
        type: "system",
        subtype: "task_started",
        task_id: "t1",
        tool_use_id: "toolu_A",
        description: "Find the schema",
        uuid: U,
        session_id: SID,
      }).drafts,
      ...tr.translate(
        assistant(
          "msg_Q",
          [
            {
              type: "tool_use",
              id: "toolu_N",
              name: "Glob",
              input: { pattern: "**/*.sql" },
            },
          ],
          "toolu_A",
        ),
      ).drafts,
      ...tr.translate({
        type: "system",
        subtype: "task_progress",
        task_id: "t1",
        tool_use_id: "toolu_A",
        description: "Find the schema",
        usage: { total_tokens: 1, tool_uses: 1, duration_ms: 1200 },
        uuid: U,
        session_id: SID,
      }).drafts,
      ...tr.translate({
        type: "tool_progress",
        tool_use_id: "toolu_N",
        tool_name: "Glob",
        parent_tool_use_id: "toolu_A",
        elapsed_time_seconds: 0.5,
        uuid: U,
        session_id: SID,
      }).drafts,
    ]);
    expect(out.map((d) => [d.kind, d.parentTool])).toEqual([
      ["tool.started", undefined],
      ["provider.passthrough", "toolu_A"],
      ["tool.started", "toolu_A"],
      ["tool.progress", undefined],
      ["tool.progress", "toolu_A"],
    ]);
    expect(out[0].data).toMatchObject({
      kind: "subagent",
      description: "Find the schema",
    });
    expect(out[3].data).toEqual({ toolId: "toolu_A", elapsedMs: 1200 });
    expect(out[4].data).toEqual({ toolId: "toolu_N", elapsedMs: 500 });
  });
});

describe("turn results", () => {
  it("[docs only] cost is the delta of the cumulative total, usage per turn, model by cost", () => {
    const tr = translator();
    const mu = (opus: number, haiku: number) => ({
      "claude-opus-5-5": {
        inputTokens: 1,
        outputTokens: 1,
        cacheReadInputTokens: 0,
        cacheCreationInputTokens: 0,
        webSearchRequests: 0,
        costUSD: opus,
        contextWindow: 1,
        maxOutputTokens: 1,
      },
      "claude-haiku-4-5": {
        inputTokens: 1,
        outputTokens: 1,
        cacheReadInputTokens: 0,
        cacheCreationInputTokens: 0,
        webSearchRequests: 0,
        costUSD: haiku,
        contextWindow: 1,
        maxOutputTokens: 1,
      },
    });
    const first = valid(
      tr.translate(
        result({ total_cost_usd: 0.3, modelUsage: mu(0.25, 0.05) }),
        turn(),
      ).drafts,
    );
    expect(first[0].data).toMatchObject({
      costUsd: 0.3,
      provider: "claude",
      model: "claude-opus-5-5",
    });
    // The second turn ran mostly on haiku: its delta decides.
    const second = valid(
      tr.translate(
        result({ total_cost_usd: 0.5, modelUsage: mu(0.27, 0.23) }),
        turn(),
      ).drafts,
    );
    expect(second[0].data).toMatchObject({
      costUsd: 0.2,
      model: "claude-haiku-4-5",
    });
    // /clear resets the running total.
    const third = valid(
      tr.translate(result({ total_cost_usd: 0.04 }), turn()).drafts,
    );
    expect(third[0].data).toMatchObject({ costUsd: 0.04 });
  });

  it("[docs only] a resume without the earlier cost reports none for its first turn", () => {
    const tr = new Translator({ cwd: CWD, mode: () => "ask", now: () => 0 });
    expect(
      tr.translate(result({ total_cost_usd: 2 }), turn()).drafts[0].data,
    ).not.toHaveProperty("costUsd");
    expect(
      tr.translate(result({ total_cost_usd: 2.5 }), turn()).drafts[0].data,
    ).toMatchObject({ costUsd: 0.5 });
  });

  it("[docs only] error subtypes become error codes", () => {
    const codes: [string, Record<string, unknown>, string][] = [
      ["max turns", { subtype: "error_max_turns", errors: [] }, "max_turns"],
      ["budget", { subtype: "error_max_budget_usd", errors: [] }, "budget"],
      [
        "execution",
        { subtype: "error_during_execution", errors: [] },
        "execution",
      ],
      ["API error", { is_error: true }, "provider_error"],
    ];
    for (const [name, over, code] of codes) {
      const d = valid(translator().translate(result(over), turn()).drafts)[0];
      expect(d.data, name).toMatchObject({ outcome: "error", errorCode: code });
    }
  });

  it("[docs only] a result for another message (a turn Claude Code started itself) passes through", () => {
    const tr = translator();
    const t = tr.translate(
      result({
        user_message_uuid: "11111111-1111-4111-8111-111111111111",
        user_message_uuids: ["11111111-1111-4111-8111-111111111111"],
      }),
      turn(),
    );
    expect(t.endsTurn).toBe(false);
    expect(valid(t.drafts)[0]).toMatchObject({
      kind: "provider.passthrough",
      data: { name: "result" },
    });
    expect(
      tr.translate(result({ user_message_uuids: [U] }), turn()).endsTurn,
    ).toBe(true);
  });
});

describe("system messages and signals", () => {
  const base = { uuid: U, session_id: SID };

  it("[docs only] api_retry, rate limits and auth errors are notices", () => {
    const tr = translator();
    const out = valid([
      ...tr.translate({
        ...base,
        type: "system",
        subtype: "api_retry",
        attempt: 1,
        max_retries: 10,
        retry_delay_ms: 500,
        error_status: 529,
        error: "overloaded",
      }).drafts,
      ...tr.translate({
        ...base,
        type: "rate_limit_event",
        rate_limit_info: {
          status: "rejected",
          resetsAt: 1764000000,
          rateLimitType: "five_hour",
        },
      }).drafts,
      ...tr.translate({
        ...base,
        type: "rate_limit_event",
        rate_limit_info: { status: "allowed_warning", utilization: 0.9 },
      }).drafts,
      ...tr.translate({
        ...base,
        type: "auth_status",
        isAuthenticating: false,
        output: [],
        error: "token expired",
      }).drafts,
    ]);
    expect(out.map((d) => (d.data as { code: string }).code)).toEqual([
      "api_retry",
      "rate_limited",
      "rate_limit_warning",
      "auth_required",
    ]);
    expect(out[0].data).toMatchObject({
      params: { attempt: "1", errorStatus: "529", error: "overloaded" },
    });
  });

  it("[docs only] an allowed rate-limit status and an unknown subtype pass through", () => {
    const tr = translator();
    const out = valid([
      ...tr.translate({
        ...base,
        type: "rate_limit_event",
        rate_limit_info: { status: "allowed" },
      }).drafts,
      ...tr.translate({
        ...base,
        type: "system",
        subtype: "session_state_changed",
        state: "idle",
      }).drafts,
    ]);
    expect(out.map((d) => (d.data as { name: string }).name)).toEqual([
      "rate_limit_event",
      "system/session_state_changed",
    ]);
  });

  it("[docs only] a mode change the CLI reports becomes session.settings", () => {
    const tr = translator({ mode: "plan" });
    const out = valid(
      tr.translate({
        ...base,
        type: "system",
        subtype: "status",
        status: null,
        permissionMode: "acceptEdits",
      }).drafts,
    );
    expect(out).toEqual([
      { kind: "session.settings", data: { mode: "accept_edits" } },
    ]);
    expect(
      tr.translate({
        ...base,
        type: "system",
        subtype: "status",
        status: "requesting",
      }).drafts,
    ).toEqual([]);
  });
});

describe("permission callback", () => {
  const req = (
    toolName: string,
    input: Record<string, unknown>,
    extra: Partial<ToolRequest> = {},
  ): ToolRequest => ({
    requestId: "req_9",
    toolName,
    input,
    toolUseID: "toolu_9",
    ...extra,
  });

  it("[docs only] allow once, allow always with the suggestions, deny with a message", () => {
    const suggestions = [
      {
        type: "addRules" as const,
        rules: [{ toolName: "Bash", ruleContent: "go test:*" }],
        behavior: "allow" as const,
        destination: "localSettings" as const,
      },
    ];
    const r = req("Bash", { command: "go test ./..." }, { suggestions });
    expect(
      permissionResult(r, { requestId: "req_9", decision: "allow_once" }),
    ).toEqual({ behavior: "allow", updatedInput: r.input });
    expect(
      permissionResult(r, { requestId: "req_9", decision: "allow_always" }),
    ).toEqual({
      behavior: "allow",
      updatedInput: r.input,
      updatedPermissions: suggestions,
    });
    expect(
      permissionResult(r, {
        requestId: "req_9",
        decision: "deny",
        message: "not now",
      }),
    ).toEqual({ behavior: "deny", message: "not now" });
  });

  it("[docs only] no always-allow when the SDK suppresses the rule", () => {
    const d = requestDraft(
      req(
        "Edit",
        { file_path: `${CWD}/x.go` },
        {
          suggestions: [
            { type: "setMode", mode: "acceptEdits", destination: "session" },
          ],
          suppressAlwaysAllowRule: true,
        },
      ),
      { cwd: CWD, mode: "ask", modeCap: "accept_edits" },
    );
    expect(valid([d])[0].data).toMatchObject({
      kind: "edit",
      path: "x.go",
      canAlwaysAllow: false,
      modeCap: "accept_edits",
    });
  });

  it("[docs only] question answers keyed by question text, labels joined, free text", () => {
    const r = req("AskUserQuestion", {
      questions: [
        {
          question: "Which cache key?",
          header: "Cache",
          multiSelect: false,
          options: [
            { label: "SHA", description: "" },
            { label: "SHA + config", description: "" },
          ],
        },
        {
          question: "Which checks?",
          header: "Checks",
          multiSelect: true,
          options: [
            { label: "lint", description: "" },
            { label: "test", description: "" },
          ],
        },
      ],
    });
    const res = questionResult(r, {
      requestId: "req_9",
      answers: [
        { questionId: "q0", optionIds: ["o1"] },
        { questionId: "q1", optionIds: ["o0", "o1"], text: "and vet" },
      ],
    });
    expect(res).toEqual({
      behavior: "allow",
      updatedInput: {
        ...r.input,
        answers: {
          "Which cache key?": "SHA + config",
          "Which checks?": "lint, test, and vet",
        },
      },
    });
    const typed = questionResult(r, { requestId: "req_9", text: "skip both" });
    expect(typed).toMatchObject({
      updatedInput: {
        answers: {
          "Which cache key?": "skip both",
          "Which checks?": "skip both",
        },
      },
    });
  });

  it("[docs only] plan approval sets the next mode for the session; rejection keeps planning", () => {
    const r = req("ExitPlanMode", { plan: "# Plan" });
    expect(
      planResult(r, {
        requestId: "req_9",
        decision: "approved",
        mode: "accept_edits",
      }),
    ).toEqual({
      behavior: "allow",
      updatedInput: r.input,
      updatedPermissions: [
        { type: "setMode", mode: "acceptEdits", destination: "session" },
      ],
    });
    expect(
      planResult(r, {
        requestId: "req_9",
        decision: "rejected",
        feedback: "split step 2",
      }),
    ).toEqual({
      behavior: "deny",
      message: "split step 2",
    });
  });

  it("[docs only] a plan without a plan field is read from the plan file the input names", () => {
    const dir = mkdtempSync(join(tmpdir(), "flow-sidecar-"));
    const file = join(dir, "plan.md");
    writeFileSync(file, "# From the file");
    expect(planText({ planFilePath: file })).toBe("# From the file");
    expect(planText({})).toBe("");
  });
});

describe("size limits", () => {
  it("cuts long tool output to head and tail within 16 KiB", () => {
    const tr = translator();
    const text = "a".repeat(20_000) + "ü".repeat(10_000) + "z".repeat(20_000);
    const d = valid(tr.translate(toolResult("toolu_L", text), turn()).drafts)[0]
      .data as { output: string; outputTruncated: boolean };
    expect(d.outputTruncated).toBe(true);
    expect(byteLength(d.output)).toBeLessThanOrEqual(CAP);
    expect(d.output.startsWith("aaa")).toBe(true);
    expect(d.output.endsWith("zzz")).toBe(true);
  });

  it("shortens a tool input only past the line limit, as the first part of its JSON", () => {
    const tr = translator();
    const small = valid(
      tr.translate(
        assistant("msg_Y", [
          {
            type: "tool_use",
            id: "toolu_Y",
            name: "Write",
            input: { file_path: "/a", content: "x".repeat(100_000) },
          },
        ]),
      ).drafts,
    );
    expect((small[0].data as { input: unknown }).input).toMatchObject({
      file_path: "/a",
    });
    const [big] = tr.translate(
      assistant("msg_Z", [
        {
          type: "tool_use",
          id: "toolu_Z",
          name: "Write",
          input: { file_path: "/a", content: "<x>".repeat(1_000_000) },
        },
      ]),
    ).drafts;
    const line = fitDraft(big) ?? "";
    expect(goSize(line)).toBeLessThanOrEqual(LINE_BUDGET);
    const d = JSON.parse(line) as EventDraft;
    expect(draftErrors(d)).toEqual([]);
    const data = d.data as {
      input: string;
      inputTruncated: boolean;
      toolId: string;
    };
    expect(data).toMatchObject({ toolId: "toolu_Z", inputTruncated: true });
    expect(data.input.startsWith('{"file_path":"/a"')).toBe(true);
  });

  it("caps a pass-through raw as the first 16 KiB of its JSON", () => {
    const tr = translator();
    const pt = valid(
      tr.translate({
        type: "system",
        subtype: "novel",
        blob: "x".repeat(100_000),
        uuid: U,
        session_id: SID,
      } as unknown as SDKMessage).drafts,
    )[0].data as { raw: string; rawTruncated: boolean };
    expect(pt.rawTruncated).toBe(true);
    expect(byteLength(pt.raw)).toBe(CAP);
  });

  it("keeps every line under the host's limit, measured as Go re-encodes it", () => {
    const text = "<&>".repeat(400_000);
    const line =
      fitDraft({
        kind: "assistant.text",
        data: { blockId: "b", text },
      }) ?? "";
    expect(goSize(line)).toBeLessThanOrEqual(LINE_BUDGET);
    expect(draftErrors(JSON.parse(line))).toEqual([]);
  });

  it("review 10: an oversized tool row shortens its summary first and always flags a changed input", () => {
    const huge = "<".repeat(400_000);
    // The command alone is over the limit: cutting it is enough, the input
    // (small) stays as it was.
    const a = JSON.parse(
      fitDraft({
        kind: "tool.started",
        data: {
          toolId: "t",
          name: "Bash",
          kind: "bash",
          command: huge,
          input: { n: 1 },
        },
      }) ?? "",
    ) as {
      data: { command: string; input: unknown; inputTruncated?: boolean };
    };
    expect(byteLength(a.data.command)).toBeLessThanOrEqual(CAP);
    expect(a.data.input).toEqual({ n: 1 });
    expect(a.data.inputTruncated).toBeUndefined();
    // Input and an unknown big field: whatever changes the input sets the flag.
    const b = JSON.parse(
      fitDraft({
        kind: "tool.started",
        data: {
          toolId: "t",
          name: "X",
          kind: "other",
          input: { a: huge, b: huge, c: huge },
        },
      }) ?? "",
    ) as EventDraft;
    expect(goSize(JSON.stringify(b))).toBeLessThanOrEqual(LINE_BUDGET);
    expect(b.data).toMatchObject({ inputTruncated: true });
    expect(draftErrors(b)).toEqual([]);
  });

  it("review 10: a request is never swapped for a notice; a notice keeps the command id", () => {
    const many = Array.from({ length: 200_000 }, (_, i) => `k${i}`);
    expect(
      fitDraft({
        kind: "permission.requested",
        data: {
          requestId: "r",
          tool: "X",
          kind: "other",
          mode: "ask",
          canAlwaysAllow: false,
          input: many,
        },
      }),
    ).toBeUndefined();
    const n = JSON.parse(
      fitDraft({
        kind: "todo.updated",
        cmd: "c-1",
        data: { items: many.map((text) => ({ text, status: "pending" })) },
      }) ?? "",
    ) as EventDraft;
    expect(n).toMatchObject({
      kind: "notice",
      cmd: "c-1",
      data: { code: "draft_too_large" },
    });
  });
});

describe("review findings in the translator", () => {
  const echoed = (uuid: string, over: Record<string, unknown> = {}) =>
    result({ user_message_uuid: uuid, user_message_uuids: [uuid], ...over });

  it("review 2: a zeroed error result moves no totals; the next turn's cost is its own", () => {
    const tr = translator();
    tr.translate(echoed(U, { total_cost_usd: 0.5 }), turn());
    const crash = tr.translate(
      echoed(U, {
        subtype: "error_during_execution",
        is_error: true,
        errors: ["x"],
        total_cost_usd: 0,
      }),
      turn(),
    ).drafts;
    expect(crash.at(-1)?.data).not.toHaveProperty("costUsd");
    const next = tr.translate(
      echoed(U, { total_cost_usd: 0.6 }),
      turn(),
    ).drafts;
    expect(next.at(-1)?.data).toMatchObject({ costUsd: 0.1 });
  });

  it("review 3: a foreign result moves no totals; its spend lands in the next turn that ends", () => {
    const tr = translator();
    tr.translate(echoed(U, { total_cost_usd: 0.5 }), turn());
    const foreign = tr.translate(
      echoed("22222222-2222-4222-8222-222222222222", { total_cost_usd: 0.7 }),
      turn(),
    );
    expect(foreign.endsTurn).toBe(false);
    expect(
      tr.translate(echoed(U, { total_cost_usd: 0.8 }), turn()).drafts.at(-1)
        ?.data,
    ).toMatchObject({
      costUsd: 0.3,
    });
  });

  it("review 4: once the CLI stamps frames, an unstamped result is foreign unless zeroed", () => {
    const tr = translator();
    tr.translate({
      ...assistant("msg_s", [{ type: "text", text: "x" }]),
      user_message_uuid: U,
    });
    expect(tr.translate(result(), turn()).endsTurn).toBe(false);
    expect(
      tr.translate(
        result({
          subtype: "error_during_execution",
          is_error: true,
          errors: [],
          total_cost_usd: 0,
        }),
        turn(),
      ).endsTurn,
    ).toBe(true);
    // A CLI that never stamps: the unstamped result is the running turn's.
    expect(translator().translate(result(), turn()).endsTurn).toBe(true);
  });

  it("review 8: two streams at once keep their own message ids", () => {
    const tr = translator();
    const ev = (parent: string | null, event: unknown) => ({
      ...stream(event),
      parent_tool_use_id: parent,
    });
    tr.translate(
      ev(null, { type: "message_start", message: { id: "msg_main" } }),
    );
    tr.translate(
      ev("toolu_A", { type: "message_start", message: { id: "msg_sub" } }),
    );
    const sub = tr.translate(
      ev("toolu_A", {
        type: "content_block_delta",
        index: 0,
        delta: { type: "text_delta", text: "s" },
      }),
    ).drafts[0];
    tr.translate(ev("toolu_A", { type: "message_stop" }));
    const main = tr.translate(
      ev(null, {
        type: "content_block_delta",
        index: 2,
        delta: { type: "text_delta", text: "m" },
      }),
    ).drafts[0];
    expect(sub.data).toMatchObject({ blockId: "msg_sub:0" });
    expect(main.data).toMatchObject({ blockId: "msg_main:2" });
  });

  it("review 9: after a resume, Task changes are not emitted until a whole list arrives", () => {
    const tr = new Translator({
      cwd: CWD,
      mode: () => "ask",
      resumed: true,
      now: () => 0,
    });
    tr.translate(
      assistant("msg_u", [
        {
          type: "tool_use",
          id: "toolu_U",
          name: "TaskUpdate",
          input: { taskId: "7", status: "completed" },
        },
      ]),
    );
    const upd = tr.translate(
      toolResult("toolu_U", "ok", {
        structured: { success: true, taskId: "7", updatedFields: ["status"] },
      }),
      turn(),
    ).drafts;
    expect(upd.map((d) => d.kind)).toEqual(["tool.finished"]);
    tr.translate(
      assistant("msg_l", [
        { type: "tool_use", id: "toolu_L", name: "TaskList", input: {} },
      ]),
    );
    const list = valid(
      tr.translate(
        toolResult("toolu_L", "1 task", {
          structured: {
            tasks: [
              { id: "7", subject: "Ship", status: "completed", blockedBy: [] },
            ],
          },
        }),
        turn(),
      ).drafts,
    );
    expect(list[1]).toEqual({
      kind: "todo.updated",
      data: { items: [{ id: "7", text: "Ship", status: "completed" }] },
    });
  });

  it("review 11 and 12: a user message's other blocks and a subagent's todo update keep the parent tool", () => {
    const tr = translator();
    const m = toolResult("toolu_q", "ok", { parent: "toolu_A" });
    (m.message.content as unknown[]).push({ type: "text", text: "note" });
    const out = tr.translate(m, turn()).drafts;
    expect(out.map((d) => [d.kind, d.parentTool])).toEqual([
      ["tool.finished", "toolu_A"],
      ["provider.passthrough", "toolu_A"],
    ]);
    // The todo update follows the open tool's parent when the result has none.
    tr.translate(
      assistant(
        "msg_t",
        [
          {
            type: "tool_use",
            id: "toolu_C2",
            name: "TaskCreate",
            input: { subject: "S", description: "d" },
          },
        ],
        "toolu_A",
      ),
    );
    const created = tr.translate(
      toolResult("toolu_C2", "ok", {
        structured: { task: { id: "1", subject: "S" } },
      }),
      turn(),
    ).drafts;
    expect(created.map((d) => [d.kind, d.parentTool])).toEqual([
      ["tool.finished", "toolu_A"],
      ["todo.updated", "toolu_A"],
    ]);
  });
});
