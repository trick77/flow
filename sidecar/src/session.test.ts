// End-to-end tests of the stdio loop against a stand-in for the SDK's query
// function: no Claude Code, no network, no credentials. Every line the
// sidecar writes is checked against contract/events.schema.json. The SDK
// behaviour the stand-in plays (one result per turn, the uuid echo,
// interrupt ending the turn with error_during_execution, canUseTool waiting)
// is taken from the SDK's documentation and types: "[docs only]" until a
// recorded real session confirms it.

import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { PassThrough, Writable } from "node:stream";

import type {
  Options,
  PermissionResult,
  PermissionUpdate,
  Query,
  SDKMessage,
  SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk";
import { describe, expect, it } from "vitest";

import type { EventDraft } from "./contract/events.gen.js";
import { parseStartSpec } from "./options.js";
import type { QueryFn } from "./session.js";
import { messageUUID } from "./session.js";
import { DraftWriter, parseLine, runSidecar } from "./stdio.js";
import { draftErrors, fixtureLines } from "./testing/schema.js";

const SID = "0193a8f2-5d1e-7c3a-9b2e-6f1d2a3b4c5d";
const CWD = "/home/flow/work/.sessions/flaky";
const SECRET = "sk-ant-test-secret";

// Queue is an async FIFO that can be closed.
class Queue<T> implements AsyncIterable<T> {
  private items: T[] = [];
  private waiters: ((r: IteratorResult<T>) => void)[] = [];
  private closed = false;
  push(v: T) {
    const w = this.waiters.shift();
    if (w) w({ value: v, done: false });
    else this.items.push(v);
  }
  close() {
    this.closed = true;
    for (const w of this.waiters.splice(0)) w({ value: undefined, done: true });
  }
  [Symbol.asyncIterator](): AsyncIterator<T> {
    return {
      next: () => {
        if (this.items.length > 0)
          return Promise.resolve({
            value: this.items.shift() as T,
            done: false,
          });
        if (this.closed)
          return Promise.resolve({ value: undefined, done: true });
        return new Promise((r) => this.waiters.push(r));
      },
    };
  }
}

// Ctx is what a stand-in script sees of one query() call.
interface Ctx {
  options: Options;
  next(): Promise<SDKUserMessage | undefined>;
  emit(m: unknown): void;
  ask(
    toolName: string,
    input: Record<string, unknown>,
    o: {
      toolUseID: string;
      requestId: string;
      suggestions?: PermissionUpdate[];
    },
  ): Promise<PermissionResult>;
  interrupted(): Promise<void>;
  modes: string[];
  models: string[];
  // efforts: each applyFlagSettings effortLevel (null: the model's own).
  efforts: (string | null)[];
  closed: boolean;
}

function fakeSDK(scripts: ((ctx: Ctx) => Promise<void>)[]) {
  const calls: Ctx[] = [];
  const query: QueryFn = ({ prompt, options }) => {
    const out = new Queue<unknown>();
    const it = prompt[Symbol.asyncIterator]();
    const abort = new AbortController();
    let onInterrupt: (() => void) | undefined;
    const ctx: Ctx = {
      options,
      modes: [],
      models: [],
      efforts: [],
      closed: false,
      next: async () => (await it.next()).value as SDKUserMessage | undefined,
      emit: (m) => out.push(m),
      ask: (toolName, input, o) => {
        const canUseTool = options.canUseTool;
        if (canUseTool === undefined) throw new Error("no canUseTool");
        return canUseTool(toolName, input, {
          signal: abort.signal,
          ...o,
        }) as Promise<PermissionResult>;
      },
      interrupted: () => new Promise((r) => (onInterrupt = r)),
    };
    const script = scripts[calls.length];
    calls.push(ctx);
    void script(ctx).then(
      () => out.close(),
      (err: unknown) =>
        out.push(err instanceof Error ? err : new Error(String(err))),
    );
    const gen = (async function* () {
      for await (const m of out) {
        if (m instanceof Error) throw m;
        yield m as SDKMessage;
      }
    })();
    return Object.assign(gen, {
      interrupt: async () => {
        onInterrupt?.();
        return undefined;
      },
      setPermissionMode: async (mode: string) => {
        // As the SDK: bypassPermissions only when allowed at the start.
        if (
          mode === "bypassPermissions" &&
          !options.allowDangerouslySkipPermissions
        ) {
          throw new Error("bypassPermissions not allowed");
        }
        ctx.modes.push(mode);
        return { mode };
      },
      setModel: async (model?: string) => {
        ctx.models.push(String(model));
      },
      applyFlagSettings: async (s: { effortLevel?: string | null }) => {
        if ("effortLevel" in s) ctx.efforts.push(s.effortLevel ?? null);
      },
      close: () => {
        ctx.closed = true;
        abort.abort();
        out.close();
      },
    }) as unknown as Query;
  };
  return { query, calls };
}

function harness(query: QueryFn, extra: { recordPath?: string } = {}) {
  const stdin = new PassThrough();
  const stdout = new PassThrough();
  const stderr = new PassThrough();
  let errText = "";
  stderr.on("data", (b: Buffer) => (errText += b.toString()));
  const drafts: EventDraft[] = [];
  const lines: string[] = [];
  let wake: (() => void) | undefined;
  createInterface({ input: stdout }).on("line", (l) => {
    lines.push(l);
    drafts.push(JSON.parse(l) as EventDraft);
    wake?.();
  });
  const done = runSidecar(
    { stdin, stdout, stderr },
    {
      query,
      env: { PATH: "/usr/bin", ANTHROPIC_API_KEY: SECRET },
      interruptGraceMs: 500,
      ...extra,
    },
  );
  let cursor = 0;
  const h = {
    drafts,
    lines,
    done,
    stderr: () => errText,
    raw(line: string) {
      stdin.write(`${line}\n`);
    },
    send(
      kind: string,
      data: unknown,
      id = `cmd-${kind}-${Math.random().toString(36).slice(2)}`,
    ) {
      h.raw(JSON.stringify({ v: 1, id, session: SID, kind, data }));
      return id;
    },
    start(over: Record<string, unknown> = {}) {
      h.raw(
        JSON.stringify({
          v: 1,
          id: "start-1",
          kind: "sidecar.start",
          data: {
            sessionId: SID,
            resume: false,
            cwd: CWD,
            mode: "ask",
            claudeAuth: "subscription_token",
            claudeExecutable: "/usr/local/bin/claude",
            settingsPath: "/etc/flow/claude-settings.json",
            mcpServers: {
              flow: {
                type: "stdio",
                command: "/opt/flow/bin/flowd",
                args: ["mcp"],
                env: { BACKEND_MCP_TOKEN: "t" },
              },
            },
            version: "0.1.0",
            ...over,
          },
        }),
      );
    },
    // until waits for the next draft (after the last one awaited) that
    // matches, and returns it.
    async until(
      pred: (d: EventDraft) => boolean,
      what = "draft",
    ): Promise<EventDraft> {
      const deadline = Date.now() + 3000;
      for (;;) {
        for (; cursor < drafts.length; cursor++) {
          if (pred(drafts[cursor])) return drafts[cursor++];
        }
        if (Date.now() > deadline)
          throw new Error(
            `timed out waiting for ${what}; got ${JSON.stringify(drafts.map((d) => d.kind))}`,
          );
        await new Promise<void>((r) => {
          wake = r;
          setTimeout(r, 50);
        });
      }
    },
    kind(k: string) {
      return h.until((d) => d.kind === k, k);
    },
    close() {
      stdin.end();
      return done;
    },
  };
  return h;
}

const base = { session_id: SID, uuid: "00000000-0000-4000-8000-0000000000ff" };

function assistant(id: string, content: unknown[]) {
  return {
    ...base,
    type: "assistant",
    parent_tool_use_id: null,
    message: { id, role: "assistant", content },
  };
}

function result(
  u: SDKUserMessage | undefined,
  over: Record<string, unknown> = {},
) {
  return {
    ...base,
    type: "result",
    subtype: "success",
    duration_ms: 1200,
    duration_api_ms: 900,
    is_error: false,
    num_turns: 1,
    result: "Done.",
    stop_reason: "end_turn",
    total_cost_usd: 0.1,
    usage: {
      input_tokens: 100,
      output_tokens: 50,
      cache_read_input_tokens: 1000,
      cache_creation_input_tokens: 10,
    },
    modelUsage: {},
    permission_denials: [],
    ...(u?.uuid !== undefined && {
      user_message_uuid: u.uuid,
      user_message_uuids: [u.uuid],
    }),
    ...over,
  };
}

function messageText(u: SDKUserMessage | undefined): unknown {
  return u?.message.content;
}

const suggestions: PermissionUpdate[] = [
  {
    type: "addRules",
    rules: [{ toolName: "Bash", ruleContent: "go test:*" }],
    behavior: "allow",
    destination: "localSettings",
  },
];

describe("stdio loop against a stand-in SDK", () => {
  it("[docs only] runs turns with tools, a permission, a question, a plan, an interrupt, a crash and a resume", async () => {
    const answers: Record<string, PermissionResult> = {};
    const seen: Record<string, unknown> = {};
    const sdk = fakeSDK([
      async (ctx) => {
        // Turn 1: text, an unknown message, a tool call that needs permission.
        let u = await ctx.next();
        seen.first = messageText(u);
        seen.firstUuid = u?.uuid;
        ctx.emit({
          ...base,
          type: "system",
          subtype: "init",
          cwd: CWD,
          model: "claude-opus-5-5",
          permissionMode: "default",
          tools: [],
          mcp_servers: [],
          apiKeySource: "none",
          claude_code_version: "2.1.240",
          slash_commands: [],
          output_style: "default",
          skills: [],
          plugins: [],
        });
        ctx.emit({
          ...base,
          type: "stream_event",
          parent_tool_use_id: null,
          event: { type: "message_start", message: { id: "msg_1" } },
        });
        ctx.emit({
          ...base,
          type: "stream_event",
          parent_tool_use_id: null,
          event: {
            type: "content_block_delta",
            index: 0,
            delta: { type: "text_delta", text: "Running " },
          },
        });
        ctx.emit({
          ...base,
          type: "stream_event",
          parent_tool_use_id: null,
          event: {
            type: "content_block_delta",
            index: 0,
            delta: { type: "text_delta", text: "the tests." },
          },
        });
        ctx.emit(
          assistant("msg_1", [{ type: "text", text: "Running the tests." }]),
        );
        ctx.emit({
          ...base,
          type: "system",
          subtype: "workspace_indexed",
          files: 1840,
        });
        ctx.emit(
          assistant("msg_1", [
            {
              type: "tool_use",
              id: "toolu_1",
              name: "Bash",
              input: { command: "go test ./...", description: "Run tests" },
            },
          ]),
        );
        answers.bash = await ctx.ask(
          "Bash",
          { command: "go test ./...", description: "Run tests" },
          { toolUseID: "toolu_1", requestId: "req_1", suggestions },
        );
        ctx.emit({
          ...base,
          type: "user",
          parent_tool_use_id: null,
          message: {
            role: "user",
            content: [
              {
                type: "tool_result",
                tool_use_id: "toolu_1",
                content: "ok",
                is_error: false,
              },
            ],
          },
          tool_use_result: { stdout: "ok", stderr: "", interrupted: false },
        });
        ctx.emit(result(u));

        // Turn 2: a question, then a plan approval.
        u = await ctx.next();
        ctx.emit(
          assistant("msg_2", [
            {
              type: "tool_use",
              id: "toolu_2",
              name: "AskUserQuestion",
              input: {},
            },
          ]),
        );
        answers.question = await ctx.ask(
          "AskUserQuestion",
          {
            questions: [
              {
                question: "Which key?",
                header: "Key",
                multiSelect: false,
                options: [
                  { label: "SHA", description: "a" },
                  { label: "SHA + config", description: "b" },
                ],
              },
            ],
          },
          { toolUseID: "toolu_2", requestId: "req_2" },
        );
        ctx.emit(
          assistant("msg_2b", [
            {
              type: "tool_use",
              id: "toolu_3",
              name: "ExitPlanMode",
              input: {},
            },
          ]),
        );
        answers.plan = await ctx.ask(
          "ExitPlanMode",
          { plan: "# Plan\n1. Do it." },
          { toolUseID: "toolu_3", requestId: "req_3" },
        );
        ctx.emit(result(u, { total_cost_usd: 0.25 }));

        // Turn 3: interrupted while a permission waits.
        u = await ctx.next();
        ctx.emit(
          assistant("msg_3", [
            {
              type: "tool_use",
              id: "toolu_4",
              name: "Bash",
              input: { command: "rm -rf build" },
            },
          ]),
        );
        const interrupted = ctx.interrupted();
        answers.interrupted = await ctx.ask(
          "Bash",
          { command: "rm -rf build" },
          { toolUseID: "toolu_4", requestId: "req_4" },
        );
        await interrupted;
        ctx.emit({
          ...base,
          type: "user",
          parent_tool_use_id: null,
          message: {
            role: "user",
            content: [
              {
                type: "tool_result",
                tool_use_id: "toolu_4",
                content: "Interrupted",
                is_error: true,
              },
            ],
          },
        });
        ctx.emit(
          result(u, {
            subtype: "error_during_execution",
            is_error: true,
            errors: [],
            total_cost_usd: 0.3,
          }),
        );

        // Turn 4: Claude Code dies mid-turn.
        await ctx.next();
        ctx.emit(
          assistant("msg_4", [
            {
              type: "tool_use",
              id: "toolu_5",
              name: "Read",
              input: { file_path: `${CWD}/go.mod` },
            },
          ]),
        );
        throw new Error("Claude Code process exited with code 1");
      },
      async (ctx) => {
        // The next message resumes the session in a new query.
        seen.resumeOptions = {
          resume: ctx.options.resume,
          sessionId: ctx.options.sessionId,
        };
        const u = await ctx.next();
        ctx.emit(result(u, { total_cost_usd: 0.35 }));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.raw("{oops");
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_bad_command",
      params: { reason: "not_json" },
    });
    h.start();

    // Turn 1.
    const m1 = h.send("message.send", {
      messageId: "0193a8f2-6000-7000-8000-000000000001",
      text: "Run the tests.",
      origin: "user",
    });
    expect((await h.kind("session.started")).data).toEqual({
      provider: "claude",
      claudeAuth: "subscription_token",
      model: "claude-opus-5-5",
      mode: "ask",
      providerSessionId: SID,
      cwd: CWD,
    });
    expect((await h.kind("assistant.text_delta")).data).toEqual({
      blockId: "msg_1:0",
      text: "Running ",
    });
    expect((await h.kind("assistant.text")).data).toEqual({
      blockId: "msg_1:0",
      text: "Running the tests.",
    });
    expect((await h.kind("provider.passthrough")).data).toMatchObject({
      provider: "claude",
      name: "system/workspace_indexed",
    });
    expect((await h.kind("tool.started")).data).toMatchObject({
      toolId: "toolu_1",
      kind: "bash",
      command: "go test ./...",
    });
    const perm = await h.kind("permission.requested");
    expect(perm.data).toMatchObject({
      requestId: "req_1",
      toolId: "toolu_1",
      kind: "command",
      mode: "ask",
      canAlwaysAllow: true,
      cwd: CWD,
    });
    h.send("permission.reply", { requestId: "nope", decision: "allow_once" });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_reply_not_pending",
      params: { requestId: "nope", reason: "unknown" },
    });
    h.send("permission.reply", {
      requestId: "req_1",
      decision: "allow_always",
    });
    expect((await h.kind("tool.finished")).data).toMatchObject({
      toolId: "toolu_1",
      status: "ok",
      output: "ok",
    });
    const end1 = await h.kind("turn.ended");
    expect(end1.data).toEqual({
      outcome: "completed",
      durationMs: 1200,
      usage: {
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 1000,
        cacheWriteTokens: 10,
      },
      costUsd: 0.1,
    });
    expect(seen.first).toBe("Run the tests.");
    expect(seen.firstUuid).toBe("0193a8f2-6000-7000-8000-000000000001");
    expect(answers.bash).toEqual({
      behavior: "allow",
      updatedInput: { command: "go test ./...", description: "Run tests" },
      updatedPermissions: [{ ...suggestions[0], destination: "session" }],
    });
    // The same reply again: already answered, not applied twice.
    h.send("permission.reply", { requestId: "req_1", decision: "deny" });
    expect((await h.kind("notice")).data).toMatchObject({
      params: { requestId: "req_1", reason: "answered" },
    });

    // Settings between turns.
    h.send("settings.set", { mode: "plan", model: "claude-sonnet-4-5" });
    await new Promise((r) => setTimeout(r, 20));
    expect(sdk.calls[0].modes).toEqual(["plan"]);
    expect(sdk.calls[0].models).toEqual(["claude-sonnet-4-5"]);

    // Turn 2.
    h.send("message.send", {
      messageId: "msg-2",
      text: "Plan it.",
      origin: "user",
    });
    const q = await h.kind("question.asked");
    expect(q.data).toEqual({
      requestId: "req_2",
      toolId: "toolu_2",
      questions: [
        {
          id: "q0",
          header: "Key",
          text: "Which key?",
          multiSelect: false,
          options: [
            { id: "o0", label: "SHA", description: "a" },
            { id: "o1", label: "SHA + config", description: "b" },
          ],
        },
      ],
    });
    h.send("question.reply", {
      requestId: "req_2",
      answers: [{ questionId: "q0", optionIds: ["o1"] }],
    });
    const plan = await h.kind("plan.proposed");
    expect(plan.data).toEqual({
      requestId: "req_3",
      toolId: "toolu_3",
      plan: "# Plan\n1. Do it.",
    });
    h.send("plan.reply", {
      requestId: "req_3",
      decision: "approved",
      mode: "accept_edits",
    });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "completed",
      costUsd: 0.15,
    });
    expect(answers.question).toMatchObject({
      behavior: "allow",
      updatedInput: { answers: { "Which key?": "SHA + config" } },
    });
    expect(answers.plan).toMatchObject({
      behavior: "allow",
      updatedPermissions: [
        { type: "setMode", mode: "acceptEdits", destination: "session" },
      ],
    });

    // Turn 3: interrupt while the permission waits.
    h.send("message.send", {
      messageId: "msg-3",
      text: "Clean up.",
      origin: "user",
    });
    const p4 = await h.kind("permission.requested");
    // After the approved plan the session continues in accept_edits.
    expect(p4.data).toMatchObject({ requestId: "req_4", mode: "accept_edits" });
    h.send("turn.interrupt", {});
    expect((await h.kind("tool.finished")).data).toMatchObject({
      toolId: "toolu_4",
      status: "interrupted",
    });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "interrupted",
      costUsd: 0.05,
    });
    expect(answers.interrupted).toEqual({
      behavior: "deny",
      message: "Interrupted by the user.",
      interrupt: true,
    });

    // Turn 4: the query dies; the turn fails, the process stays usable.
    h.send("message.send", {
      messageId: "msg-4",
      text: "Read go.mod.",
      origin: "user",
    });
    expect((await h.kind("tool.started")).data).toMatchObject({
      toolId: "toolu_5",
      path: "go.mod",
    });
    expect((await h.kind("tool.finished")).data).toMatchObject({
      toolId: "toolu_5",
      status: "interrupted",
    });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "error",
      errorCode: "provider_exited",
    });

    // Turn 5: a new query resumes the session.
    h.send("message.send", {
      messageId: "msg-5",
      text: "Again.",
      origin: "user",
    });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "completed",
      costUsd: 0.05,
    });
    expect(seen.resumeOptions).toEqual({ resume: SID, sessionId: undefined });

    await h.close();
    expect(sdk.calls[1].closed).toBe(true);
    for (const d of h.drafts) expect(draftErrors(d), d.kind).toEqual([]);
    // Turn ends match the messages sent, one each.
    expect(h.drafts.filter((d) => d.kind === "turn.ended")).toHaveLength(5);
    expect(h.drafts.filter((d) => d.cmd === m1)).toHaveLength(0);
    expect(h.stderr()).not.toContain(SECRET);
  });

  it("[docs only] starts Claude Code with the options the architecture prescribes", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start({
      modeCap: "accept_edits",
      env: { GIT_AUTHOR_NAME: "Dev" },
      additionalDirectories: ["/srv/flow-home/cache"],
    });
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    const o = sdk.calls[0].options;
    expect(o).toMatchObject({
      cwd: CWD,
      sessionId: SID,
      pathToClaudeCodeExecutable: "/usr/local/bin/claude",
      includePartialMessages: true,
      settingSources: ["user", "project", "local"],
      settings: "/etc/flow/claude-settings.json",
      systemPrompt: { type: "preset", preset: "claude_code" },
      permissionMode: "default",
      allowDangerouslySkipPermissions: false,
      additionalDirectories: ["/srv/flow-home/cache"],
      mcpServers: {
        flow: {
          type: "stdio",
          command: "/opt/flow/bin/flowd",
          args: ["mcp"],
          env: { BACKEND_MCP_TOKEN: "t" },
        },
      },
    });
    expect(o.resume).toBeUndefined();
    expect(o.extraArgs).toBeUndefined();
    expect(o.env).toMatchObject({
      PATH: "/usr/bin",
      ANTHROPIC_API_KEY: SECRET,
      GIT_AUTHOR_NAME: "Dev",
      CLAUDE_CODE_ENTRYPOINT: "flow",
      CLAUDE_AGENT_SDK_CLIENT_APP: "flow/0.1.0",
      DISABLE_AUTOUPDATER: "1",
    });
  });

  it("[docs only] a resume start passes resume, never sessionId", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u, { total_cost_usd: 1.5 }));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start({ resume: true, priorCostUsd: 1.2 });
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    expect((await h.kind("turn.ended")).data).toMatchObject({ costUsd: 0.3 });
    await h.close();
    expect(sdk.calls[0].options.resume).toBe(SID);
    expect(sdk.calls[0].options.sessionId).toBeUndefined();
  });

  it("appends the host's layout note to Claude Code's prompt, not recorded once, and passes the worktrees outside cwd", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    const note = "The session's repositories:\n- /w/.sessions/s/api: acme/api";
    h.start({
      additionalDirectories: ["/w/.sessions/s/web"],
      appendSystemPrompt: note,
    });
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    expect(sdk.calls[0].options).toMatchObject({
      systemPrompt: {
        type: "preset",
        preset: "claude_code",
        append: note,
        snapshot: false,
      },
      additionalDirectories: ["/w/.sessions/s/web"],
    });
  });

  it("refuses a start whose appendSystemPrompt is not a string", () => {
    const data = {
      sessionId: SID,
      resume: false,
      cwd: CWD,
      mode: "ask",
      claudeExecutable: "/usr/local/bin/claude",
      settingsPath: "/etc/flow/claude-settings.json",
    };
    expect(typeof parseStartSpec(data)).toBe("object");
    expect(parseStartSpec({ ...data, appendSystemPrompt: 1 })).toBe(
      "appendSystemPrompt",
    );
    expect(typeof parseStartSpec({ ...data, effort: "xhigh" })).toBe("object");
    expect(parseStartSpec({ ...data, effort: "huge" })).toBe("effort");
  });

  it("[docs only] host.shutdown while a permission waits: the request is denied, the turn interrupted, the process ends", async () => {
    const got: PermissionResult[] = [];
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        const interrupted = ctx.interrupted();
        // No tool_use message: the request is shown after the hold anyway.
        got.push(
          await ctx.ask(
            "Write",
            { file_path: `${CWD}/x`, content: "" },
            { toolUseID: "toolu_9", requestId: "req_9" },
          ),
        );
        await interrupted;
        ctx.emit(
          result(u, {
            subtype: "error_during_execution",
            is_error: true,
            errors: [],
          }),
        );
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    expect((await h.kind("permission.requested")).data).toMatchObject({
      kind: "write",
      path: "x",
    });
    h.send("host.shutdown", { reason: "stop" });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "interrupted",
    });
    await h.done;
    expect(got).toEqual([
      {
        behavior: "deny",
        message: "Interrupted by the user.",
        interrupt: true,
      },
    ]);
    expect(sdk.calls[0].closed).toBe(true);
  });

  it("[docs only] shows concurrent requests one at a time; a withdrawn one is denied without a draft", async () => {
    const got: Record<string, PermissionResult> = {};
    let withdraw: (() => void) | undefined;
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(
          assistant("msg_c", [
            {
              type: "tool_use",
              id: "toolu_a",
              name: "Bash",
              input: { command: "a" },
            },
            {
              type: "tool_use",
              id: "toolu_b",
              name: "Bash",
              input: { command: "b" },
            },
            {
              type: "tool_use",
              id: "toolu_c",
              name: "Bash",
              input: { command: "c" },
            },
          ]),
        );
        const ac = new AbortController();
        withdraw = () => ac.abort();
        const canUseTool = ctx.options.canUseTool;
        if (canUseTool === undefined) throw new Error("no canUseTool");
        const [a, b, c] = await Promise.all([
          ctx.ask(
            "Bash",
            { command: "a" },
            { toolUseID: "toolu_a", requestId: "req_a" },
          ),
          canUseTool(
            "Bash",
            { command: "b" },
            { signal: ac.signal, toolUseID: "toolu_b", requestId: "req_b" },
          ),
          ctx.ask(
            "Bash",
            { command: "c" },
            { toolUseID: "toolu_c", requestId: "req_c" },
          ),
        ]);
        Object.assign(got, { a, b, c });
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    expect((await h.kind("permission.requested")).data).toMatchObject({
      requestId: "req_a",
    });
    await new Promise((r) => setTimeout(r, 50));
    // One request at a time: b and c wait.
    expect(
      h.drafts.filter((d) => d.kind === "permission.requested"),
    ).toHaveLength(1);
    h.send("permission.reply", { requestId: "req_a", decision: "allow_once" });
    expect((await h.kind("permission.requested")).data).toMatchObject({
      requestId: "req_b",
    });
    // The SDK withdraws b while it is shown, c queued: b is closed as
    // expired (the host frees its slot), then c follows at once.
    withdraw?.();
    const next = await h.until(
      (d) =>
        d.kind === "permission.resolved" || d.kind === "permission.requested",
      "expired b",
    );
    expect(next).toEqual({
      kind: "permission.resolved",
      data: { requestId: "req_b", decision: "expired" },
    });
    expect((await h.kind("permission.requested")).data).toMatchObject({
      requestId: "req_c",
    });
    // A late reply for b is acknowledged, not applied.
    h.send("permission.reply", { requestId: "req_b", decision: "allow_once" });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_reply_not_pending",
      params: { requestId: "req_b", reason: "answered" },
    });
    h.send("permission.reply", {
      requestId: "req_c",
      decision: "deny",
      message: "no",
    });
    await h.kind("turn.ended");
    await h.close();
    expect(got.a).toMatchObject({ behavior: "allow" });
    expect(got.b).toMatchObject({ behavior: "deny", interrupt: true });
    expect(got.c).toEqual({ behavior: "deny", message: "no" });
    expect(h.drafts.filter((d) => d.kind.endsWith(".resolved"))).toHaveLength(
      1,
    );
  });

  it("[docs only] a question withdrawn with nothing queued is closed as expired; the turn goes on", async () => {
    let got: PermissionResult | undefined;
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(
          assistant("msg_q", [
            {
              type: "tool_use",
              id: "toolu_q",
              name: "AskUserQuestion",
              input: {},
            },
          ]),
        );
        const ac = new AbortController();
        const canUseTool = ctx.options.canUseTool;
        if (canUseTool === undefined) throw new Error("no canUseTool");
        const asked = canUseTool(
          "AskUserQuestion",
          {
            questions: [
              {
                question: "Which?",
                header: "Q",
                multiSelect: false,
                options: [{ label: "A", description: "" }],
              },
            ],
          },
          { signal: ac.signal, toolUseID: "toolu_q", requestId: "req_q" },
        );
        await new Promise((r) => setTimeout(r, 30));
        ac.abort();
        got = (await asked) ?? undefined;
        ctx.emit(assistant("msg_q2", [{ type: "text", text: "moving on" }]));
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m", text: "go", origin: "user" });
    await h.kind("question.asked");
    expect(await h.kind("question.resolved")).toEqual({
      kind: "question.resolved",
      data: { requestId: "req_q", outcome: "expired" },
    });
    expect((await h.kind("assistant.text")).data).toMatchObject({
      text: "moving on",
    });
    h.send("question.reply", { requestId: "req_q", text: "late" });
    expect((await h.kind("notice")).data).toMatchObject({
      params: { requestId: "req_q", reason: "answered" },
    });
    await h.close();
    expect(got).toMatchObject({ behavior: "deny" });
    for (const d of h.drafts) expect(draftErrors(d), d.kind).toEqual([]);
  });

  it("review 1: a when-idle shutdown lets the turn finish, still takes replies, and refuses new messages", async () => {
    let answer: PermissionResult | undefined;
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(
          assistant("msg_w", [
            {
              type: "tool_use",
              id: "toolu_w",
              name: "Bash",
              input: { command: "make" },
            },
          ]),
        );
        answer = await ctx.ask(
          "Bash",
          { command: "make" },
          { toolUseID: "toolu_w", requestId: "req_w" },
        );
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m1", text: "go", origin: "user" });
    await h.kind("permission.requested");
    h.send("host.shutdown", { reason: "upgrade", whenIdle: true });
    h.send("message.send", { messageId: "m2", text: "more", origin: "user" });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_shutting_down",
    });
    h.send("permission.reply", { requestId: "req_w", decision: "allow_once" });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "completed",
    });
    await h.done;
    expect(answer).toMatchObject({ behavior: "allow" });
    expect(sdk.calls[0].closed).toBe(true);
  });

  it("review 1: an immediate stop cuts a pending when-idle shutdown short", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        await ctx.interrupted();
        ctx.emit(
          result(u, {
            subtype: "error_during_execution",
            is_error: true,
            errors: [],
          }),
        );
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m1", text: "go", origin: "user" });
    await new Promise((r) => setTimeout(r, 20));
    h.send("host.shutdown", { reason: "upgrade", whenIdle: true });
    await new Promise((r) => setTimeout(r, 20));
    h.raw("");
    await h.close(); // stdin closes: the host is gone
    expect(h.drafts.find((d) => d.kind === "turn.ended")?.data).toMatchObject({
      outcome: "interrupted",
    });
    expect(sdk.calls[0].closed).toBe(true);
  });

  it("review 6 and 7: a cap from settings.set reaches requests and a restarted query; a refused mode is undone", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
        ctx.emit(
          assistant("msg_k", [
            {
              type: "tool_use",
              id: "toolu_k",
              name: "Bash",
              input: { command: "x" },
            },
          ]),
        );
        await ctx.ask(
          "Bash",
          { command: "x" },
          { toolUseID: "toolu_k", requestId: "req_k" },
        );
        throw new Error("exited");
      },
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start({ mode: "ask", modeCap: "accept_edits" });
    h.send("message.send", { messageId: "m1", text: "a", origin: "user" });
    await h.kind("turn.ended");
    // The first query was started with the cap accept_edits: bypass is refused.
    const cmd = h.send("settings.set", {
      mode: "full_access",
      modeCap: "full_access",
    });
    expect(await h.kind("notice")).toMatchObject({
      cmd,
      data: { code: "mode_change_failed" },
    });
    expect(await h.kind("session.settings")).toMatchObject({
      cmd,
      data: { mode: "ask" },
    });
    h.send("message.send", { messageId: "m2", text: "b", origin: "user" });
    expect((await h.kind("permission.requested")).data).toMatchObject({
      mode: "ask",
      modeCap: "full_access",
    });
    h.send("permission.reply", { requestId: "req_k", decision: "allow_once" });
    await h.kind("turn.ended");
    h.send("message.send", { messageId: "m3", text: "c", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    expect(sdk.calls[0].options.allowDangerouslySkipPermissions).toBe(false);
    expect(sdk.calls[1].options).toMatchObject({
      allowDangerouslySkipPermissions: true,
      permissionMode: "default",
    });
  });

  it("a refused model is undone on record and for the next query", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
        throw new Error("exited");
      },
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const query: QueryFn = (params) =>
      Object.assign(sdk.query(params), {
        setModel: async () => {
          throw new Error("model not available");
        },
      });
    const h = harness(query);
    h.start({ model: "claude-opus-5-5" });
    h.send("message.send", { messageId: "m1", text: "a", origin: "user" });
    await h.kind("turn.ended");
    const cmd = h.send("settings.set", { model: "claude-nope" });
    expect(await h.kind("notice")).toMatchObject({
      cmd,
      data: { code: "model_change_failed" },
    });
    expect(await h.kind("session.settings")).toMatchObject({
      cmd,
      data: { model: "claude-opus-5-5" },
    });
    // The first query exits with the next turn; the one after starts a new
    // query on the model in force.
    h.send("message.send", { messageId: "m2", text: "b", origin: "user" });
    await h.kind("turn.ended");
    h.send("message.send", { messageId: "m3", text: "c", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    expect(sdk.calls[1].options.model).toBe("claude-opus-5-5");
  });

  it("an effort starts the query, changes a running one, and default passes none", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        ctx.emit(result(await ctx.next()));
        await ctx.next();
        throw new Error("exited");
      },
      async (ctx) => {
        ctx.emit(result(await ctx.next()));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start({ effort: "high" });
    h.send("message.send", { messageId: "m1", text: "a", origin: "user" });
    await h.kind("turn.ended");
    h.send("settings.set", { effort: "max" });
    h.send("settings.set", { effort: "default" });
    await new Promise((r) => setTimeout(r, 20));
    // Back to default: the query after a restart passes no effort.
    h.send("message.send", { messageId: "m2", text: "b", origin: "user" });
    await h.kind("turn.ended");
    h.send("message.send", { messageId: "m3", text: "c", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    expect(sdk.calls[0].options.effort).toBe("high");
    expect(sdk.calls[0].efforts).toEqual(["max", null]);
    expect(sdk.calls[1].options).not.toHaveProperty("effort");
  });

  it("a refused effort is put back on record, default included", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
        throw new Error("exited");
      },
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const query: QueryFn = (params) =>
      Object.assign(sdk.query(params), {
        applyFlagSettings: async () => {
          throw new Error("flag settings refused");
        },
      });
    const h = harness(query);
    h.start(); // no effort: Claude Code's own
    h.send("message.send", { messageId: "m1", text: "a", origin: "user" });
    await h.kind("turn.ended");
    const cmd = h.send("settings.set", { effort: "low" });
    expect(await h.kind("notice")).toMatchObject({
      cmd,
      data: { code: "effort_change_failed", params: { effort: "low" } },
    });
    expect(await h.kind("session.settings")).toMatchObject({
      cmd,
      data: { effort: "default" },
    });
    h.send("message.send", { messageId: "m2", text: "b", origin: "user" });
    await h.kind("turn.ended");
    h.send("message.send", { messageId: "m3", text: "c", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    expect(sdk.calls[0].options).not.toHaveProperty("effort");
    expect(sdk.calls[1].options).not.toHaveProperty("effort");
  });

  it("review 10: a request too large to show is denied, not left waiting", async () => {
    let answer: PermissionResult | undefined;
    const many = Array.from({ length: 200_000 }, (_, i) => `k${i}`);
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        answer = await ctx.ask(
          "Workbench",
          { keys: many },
          { toolUseID: "toolu_big", requestId: "req_big" },
        );
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m", text: "go", origin: "user" });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "request_too_large",
      params: { requestId: "req_big" },
    });
    await h.kind("turn.ended");
    await h.close();
    expect(answer).toMatchObject({ behavior: "deny" });
    expect(h.drafts.some((d) => d.kind === "permission.requested")).toBe(false);
  });

  it("review 13: a message the mapping cannot handle is passed through and the turn goes on", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit({
          ...base,
          type: "assistant",
          parent_tool_use_id: null,
          message: null,
        });
        ctx.emit(assistant("msg_ok", [{ type: "text", text: "still here" }]));
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m", text: "go", origin: "user" });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_translate_failed",
      params: { name: "assistant" },
    });
    expect((await h.kind("provider.passthrough")).data).toMatchObject({
      name: "assistant",
    });
    expect((await h.kind("assistant.text")).data).toMatchObject({
      text: "still here",
    });
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "completed",
    });
    await h.close();
  });

  it("review 14: Flow's DISABLE_AUTOUPDATER wins over both environments", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query);
    h.start({ env: { DISABLE_AUTOUPDATER: "0" } });
    h.send("message.send", { messageId: "m", text: "go", origin: "user" });
    await h.kind("turn.ended");
    await h.close();
    expect(sdk.calls[0].options.env?.DISABLE_AUTOUPDATER).toBe("1");
  });

  it("ends a turn itself when Claude Code ignores the interrupt", async () => {
    const sdk = fakeSDK([
      async (ctx) => {
        await ctx.next();
        await new Promise(() => {}); // never answers
      },
    ]);
    const h = harness(sdk.query);
    h.start();
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    await new Promise((r) => setTimeout(r, 20));
    h.send("turn.interrupt", {});
    expect((await h.kind("turn.ended")).data).toMatchObject({
      outcome: "interrupted",
    });
    expect(sdk.calls[0].closed).toBe(true);
    await h.close();
  });

  it("answers commands before the start, unknown kinds and broken payloads with notices", async () => {
    const h = harness(fakeSDK([]).query);
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_bad_command",
      params: { reason: "not_started" },
    });
    h.start({ mode: "everything" });
    expect((await h.kind("notice")).data).toMatchObject({
      params: { reason: "start.mode" },
    });
    h.start();
    h.send("gate.update", { state: "queued", position: 1 });
    expect((await h.kind("notice")).data).toMatchObject({
      code: "sidecar_command_skipped",
      params: { reason: "kind", kind: "gate.update" },
    });
    h.raw(
      JSON.stringify({
        v: 2,
        id: "x",
        session: SID,
        kind: "turn.interrupt",
        data: {},
      }),
    );
    expect((await h.kind("notice")).data).toMatchObject({
      params: { reason: "version" },
    });
    const bad = h.send("message.send", { messageId: "m" });
    const n = await h.kind("notice");
    expect(n).toMatchObject({
      cmd: bad,
      data: { code: "sidecar_bad_command", params: { reason: "data.text" } },
    });
    h.raw("[1,2]");
    expect((await h.kind("notice")).data).toMatchObject({
      params: { reason: "not_object" },
    });
    await h.close();
    for (const d of h.drafts) expect(draftErrors(d)).toEqual([]);
  });

  it("records every raw SDK message and callback when asked to", async () => {
    const dir = mkdtempSync(join(tmpdir(), "flow-sidecar-rec-"));
    const file = join(dir, "session.jsonl");
    const sdk = fakeSDK([
      async (ctx) => {
        const u = await ctx.next();
        ctx.emit(
          assistant("msg_r", [
            {
              type: "tool_use",
              id: "toolu_r",
              name: "Bash",
              input: { command: "ls" },
            },
          ]),
        );
        await ctx.ask(
          "Bash",
          { command: "ls" },
          { toolUseID: "toolu_r", requestId: "req_r" },
        );
        ctx.emit(result(u));
        await ctx.next();
      },
    ]);
    const h = harness(sdk.query, { recordPath: file });
    h.start();
    h.send("message.send", { messageId: "m", text: "hi", origin: "user" });
    await h.kind("permission.requested");
    h.send("permission.reply", { requestId: "req_r", decision: "allow_once" });
    await h.kind("turn.ended");
    await h.close();
    const rec = readFileSync(file, "utf8")
      .trim()
      .split("\n")
      .map((l) => JSON.parse(l) as Record<string, unknown>);
    const kinds = rec.map((r) => Object.keys(r).filter((k) => k !== "ts")[0]);
    expect(kinds.sort()).toEqual(["callback", "message", "message", "reply"]);
    expect(rec.find((r) => r.callback !== undefined)).toMatchObject({
      callback: "canUseTool",
      toolName: "Bash",
      options: { requestId: "req_r", toolUseID: "toolu_r" },
    });
  });
});

describe("protocol", () => {
  it("passes exactly the sidecar kinds of commands.jsonl", () => {
    for (const c of fixtureLines<{ id: string; kind: string }>(
      "commands.jsonl",
    )) {
      const l = parseLine(JSON.stringify(c));
      const sidecar = [
        "message.send",
        "turn.interrupt",
        "permission.reply",
        "question.reply",
        "plan.reply",
        "settings.set",
        "host.shutdown",
      ];
      expect(l.type, c.kind).toBe(
        sidecar.includes(c.kind) ? "command" : "skipped",
      );
    }
  });

  it("derives a stable uuid for a message id that is not one", () => {
    const a = messageUUID("msg-2", "cmd-1");
    expect(a).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
    expect(messageUUID("msg-2", "cmd-1")).toBe(a);
    expect(messageUUID("msg-2", "cmd-2")).not.toBe(a);
  });

  it("keeps line order and waits for a full pipe to drain", async () => {
    const got: string[] = [];
    let slow = true;
    const out = new Writable({
      highWaterMark: 64,
      write(chunk: Buffer, _enc, cb) {
        got.push(chunk.toString());
        if (slow) setTimeout(cb, 1);
        else cb();
      },
    });
    const w = new DraftWriter(out);
    const writes = Array.from({ length: 200 }, (_, i) =>
      w.write({ kind: "notice", data: { code: `n${i}`, level: "info" } }),
    );
    slow = false;
    await Promise.all(writes);
    await w.flush();
    const lines = got.join("").split("\n").filter(Boolean);
    expect(lines).toHaveLength(200);
    expect(
      lines.map((l) => (JSON.parse(l) as { data: { code: string } }).data.code),
    ).toEqual(Array.from({ length: 200 }, (_, i) => `n${i}`));
  });
});
