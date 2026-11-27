// Session drives one Claude Code session through the SDK in streaming-input
// mode and relays between it and the host: commands in, drafts out. It
// decides nothing itself; the permission callback waits for the owner's
// reply without a timeout of its own.

import { createHash } from "node:crypto";

import type {
  CanUseTool,
  PermissionResult,
  Query,
  SDKMessage,
  SDKUserMessage,
  Options,
} from "@anthropic-ai/claude-agent-sdk";

import type {
  EventDraft,
  PermissionMode,
  PermissionResolved,
  PlanResolved,
  QuestionResolved,
  TypedCommand,
} from "./contract/events.gen.js";
import { buildOptions, type StartSpec } from "./options.js";
import { toSDKMode, fromSDKMode } from "./modes.js";
import {
  permissionResult,
  planResult,
  questionResult,
  requestDraft,
  requestKind,
  type RequestKind,
  type ToolRequest,
} from "./requests.js";
import {
  messageName,
  notice,
  passthrough,
  Translator,
  type Turn,
} from "./translate.js";

export type QueryFn = (params: {
  prompt: AsyncIterable<SDKUserMessage>;
  options: Options;
}) => Query;

export interface SessionDeps {
  query: QueryFn;
  // emit resolves false when the draft could not be written (too large).
  emit: (d: EventDraft) => Promise<boolean>;
  log: (msg: string) => void;
  // record gets every raw SDK message and callback (recorder mode).
  record?: (entry: Record<string, unknown>) => void;
  env: Record<string, string | undefined>;
  stderr: (data: string) => void;
  now?: () => number;
  // interruptGraceMs: how long a turn may take to end after interrupt()
  // before the sidecar ends it itself and closes the query.
  interruptGraceMs?: number;
}

// Channel is the SDK's prompt stream: user messages pushed as turns start.
class Channel<T> implements AsyncIterable<T> {
  private items: T[] = [];
  private waiters: ((r: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(v: T): void {
    if (this.closed) return;
    const w = this.waiters.shift();
    if (w) w({ value: v, done: false });
    else this.items.push(v);
  }

  close(): void {
    this.closed = true;
    for (const w of this.waiters.splice(0)) w({ value: undefined, done: true });
  }

  [Symbol.asyncIterator](): AsyncIterator<T> {
    return {
      next: () => {
        const v = this.items.shift();
        if (v !== undefined) return Promise.resolve({ value: v, done: false });
        if (this.closed)
          return Promise.resolve({ value: undefined, done: true });
        return new Promise((resolve) => this.waiters.push(resolve));
      },
      return: () => {
        this.close();
        return Promise.resolve({ value: undefined, done: true });
      },
    };
  }
}

interface RunningTurn extends Turn {
  cmd: string;
  startedAt: number;
}

interface Pending {
  req: ToolRequest;
  kind: RequestKind;
  // shown: its draft is out, so the owner can answer it.
  shown: boolean;
  resolve: (r: PermissionResult) => void;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// messageUUID is the uuid the SDK user message carries (and its result
// echoes): the message id when it is a UUID, else one derived from the
// command id, so it never depends on this process.
export function messageUUID(messageId: string, cmd: string): string {
  if (UUID.test(messageId)) return messageId.toLowerCase();
  const h = createHash("sha256").update(`flow-message:${cmd}`).digest("hex");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-5${h.slice(13, 16)}-${((parseInt(h[16], 16) & 3) | 8).toString(16)}${h.slice(17, 20)}-${h.slice(20, 32)}`;
}

const INTERRUPTED = "Interrupted by the user.";
const CANCELLED = "The request was cancelled.";
const TOO_LARGE = "The request was too large to show to the user.";

export class Session {
  private readonly deps: SessionDeps;
  private readonly spec: StartSpec;
  private readonly translator: Translator;
  private readonly now: () => number;
  private mode: PermissionMode;
  private modeCap: PermissionMode | undefined;
  private model: string | undefined;
  private resumeNext: boolean;
  private q: Query | undefined;
  private input: Channel<SDKUserMessage> | undefined;
  private pump: Promise<void> | undefined;
  private turn: RunningTurn | undefined;
  private active: Pending | undefined;
  private readonly queue: Pending[] = [];
  private readonly answered = new Set<string>();
  private idleWaiters: (() => void)[] = [];
  // Tool rows out in the running turn, and the request held for its row.
  private readonly startedTools = new Set<string>();
  private holding: Pending | undefined;
  private holdTimer: ReturnType<typeof setTimeout> | undefined;
  private interruptTimer: ReturnType<typeof setTimeout> | undefined;
  // draining: a shutdown waits for the running turn (whenIdle) and takes no
  // new message; stopping: the session is ending now.
  private draining = false;
  private stopping = false;
  private wakeStop: (() => void) | undefined;
  private closing: Promise<void> | undefined;

  constructor(spec: StartSpec, deps: SessionDeps) {
    this.spec = spec;
    this.deps = deps;
    this.now = deps.now ?? Date.now;
    this.mode = spec.mode;
    this.modeCap = spec.modeCap;
    this.model = spec.model;
    this.resumeNext = spec.resume;
    this.translator = new Translator({
      cwd: spec.cwd,
      ...(spec.claudeAuth !== undefined && { claudeAuth: spec.claudeAuth }),
      modeCap: () => this.modeCap,
      resumed: spec.resume,
      mode: () => this.mode,
      ...(spec.resume
        ? spec.priorCostUsd !== undefined && { priorCostUsd: spec.priorCostUsd }
        : { priorCostUsd: 0 }),
      now: this.now,
    });
  }

  get shuttingDown(): boolean {
    return this.stopping;
  }

  // handle applies one command of a sidecar kind. It never blocks the
  // reader: what takes time (interrupt, mode changes) runs on its own.
  handle(c: TypedCommand): void {
    const refused =
      c.kind !== "host.shutdown" &&
      (this.stopping || (this.draining && c.kind === "message.send"));
    if (refused) {
      void this.emit(
        notice("sidecar_shutting_down", "warning", { kind: c.kind }),
        c.id,
      );
      return;
    }
    switch (c.kind) {
      case "message.send":
        this.send(c.id, c.data.messageId, c.data.text);
        return;
      case "turn.interrupt":
        void this.interrupt();
        return;
      case "permission.reply":
        this.settle(c.id, "permission", c.data.requestId, (req) => {
          const r = permissionResult(req, c.data);
          if (r.behavior === "deny") this.turn?.denied.add(req.toolUseID);
          return r;
        });
        return;
      case "question.reply":
        this.settle(c.id, "question", c.data.requestId, (req) =>
          questionResult(req, c.data),
        );
        return;
      case "plan.reply":
        this.settle(c.id, "plan", c.data.requestId, (req) => {
          if (c.data.decision === "approved" && c.data.mode !== undefined) {
            this.mode = c.data.mode;
          }
          return planResult(req, c.data);
        });
        return;
      case "settings.set":
        if (c.data.modeCap !== undefined) this.modeCap = c.data.modeCap;
        void this.settings(c.id, c.data.mode, c.data.model);
        return;
      case "host.shutdown":
        void this.shutdown(c.data.whenIdle === true);
        return;
      default:
        void this.emit(
          notice("sidecar_command_skipped", "warning", { kind: c.kind }),
          c.id,
        );
    }
  }

  private emit(d: EventDraft, cmd?: string): Promise<boolean> {
    if (cmd !== undefined) d.cmd = cmd;
    return this.deps.emit(d);
  }

  private send(cmd: string, messageId: string, text: string): void {
    if (this.turn !== undefined) {
      void this.emit(notice("sidecar_busy", "error", { messageId }), cmd);
      return;
    }
    const uuid = messageUUID(messageId, cmd);
    this.turn = {
      uuid,
      cmd,
      startedAt: this.now(),
      interrupted: false,
      denied: new Set(),
    };
    this.ensureQuery();
    this.input?.push({
      type: "user",
      message: { role: "user", content: text },
      parent_tool_use_id: null,
      uuid: uuid as SDKUserMessage["uuid"],
    });
  }

  private ensureQuery(): void {
    if (this.q !== undefined) return;
    const input = new Channel<SDKUserMessage>();
    const canUseTool: CanUseTool = (toolName, toolInput, opts) =>
      this.canUseTool(toolName, toolInput, opts);
    const options = buildOptions(this.spec, {
      resume: this.resumeNext,
      mode: this.mode,
      ...(this.modeCap !== undefined && { modeCap: this.modeCap }),
      ...(this.model !== undefined && { model: this.model }),
      canUseTool,
      env: this.deps.env,
      stderr: this.deps.stderr,
    });
    this.input = input;
    try {
      this.q = this.deps.query({ prompt: input, options });
    } catch (err) {
      this.deps.log(`query failed to start: ${errorText(err)}`);
      this.input = undefined;
      input.close();
      void this.lost();
      return;
    }
    const q = this.q;
    this.pump = this.consume(q, input);
  }

  private async consume(
    q: Query,
    input: Channel<SDKUserMessage>,
  ): Promise<void> {
    try {
      for await (const m of q) {
        await this.message(m);
      }
    } catch (err) {
      this.deps.log(`query ended with an error: ${errorText(err)}`);
    }
    input.close();
    if (this.q === q) {
      this.q = undefined;
      this.input = undefined;
    }
    await this.lost();
  }

  private async message(m: SDKMessage): Promise<void> {
    this.deps.record?.({ message: m });
    if (m.type === "system" && m.subtype === "init") {
      // The session exists now: a restarted query resumes it.
      this.resumeNext = true;
    }
    let t;
    try {
      t = this.translator.translate(m, this.turn);
    } catch (err) {
      // A message the mapping chokes on is shown as is; the turn goes on.
      this.deps.log(`translating ${messageName(m)} failed: ${errorText(err)}`);
      t = {
        drafts: [
          notice("sidecar_translate_failed", "warning", {
            name: messageName(m),
          }),
          passthrough(messageName(m), m),
        ],
        endsTurn: false,
      };
    }
    for (const d of t.drafts) {
      // The host sends the next message as soon as it reads turn.ended: the
      // turn is over here before that line goes out.
      if (d.kind === "turn.ended" && t.endsTurn) this.endTurn();
      if (d.kind === "session.settings") {
        const mode = (d.data as { mode?: PermissionMode }).mode;
        if (mode !== undefined) this.mode = mode;
      }
      await this.emit(d);
      if (d.kind === "tool.started")
        this.toolStarted((d.data as { toolId: string }).toolId);
    }
  }

  // lost ends a turn whose query is gone (Claude Code exited or the SDK
  // failed): the next message starts a new query that resumes the session.
  private async lost(): Promise<void> {
    this.cancelRequests(CANCELLED);
    const turn = this.turn;
    if (turn === undefined) return;
    for (const d of this.translator.closeOpenTools()) await this.emit(d);
    this.endTurn();
    await this.emit({
      kind: "turn.ended",
      data: turn.interrupted
        ? { outcome: "interrupted", durationMs: this.now() - turn.startedAt }
        : {
            outcome: "error",
            durationMs: this.now() - turn.startedAt,
            errorCode: "provider_exited",
          },
    });
  }

  private endTurn(): void {
    this.turn = undefined;
    if (this.interruptTimer !== undefined) clearTimeout(this.interruptTimer);
    this.interruptTimer = undefined;
    // Requests still open belong to the ended turn.
    this.cancelRequests(CANCELLED);
    this.startedTools.clear();
    for (const w of this.idleWaiters.splice(0)) w();
  }

  private idle(): Promise<void> {
    if (this.turn === undefined) return Promise.resolve();
    return new Promise((resolve) => this.idleWaiters.push(resolve));
  }

  private async interrupt(): Promise<void> {
    const turn = this.turn;
    if (turn === undefined) return;
    turn.interrupted = true;
    // Requests waiting for the owner are answered as the interrupt they are.
    this.cancelRequests(INTERRUPTED, true);
    const q = this.q;
    if (q === undefined) return;
    if (this.interruptTimer === undefined) {
      this.interruptTimer = setTimeout(() => {
        if (this.turn !== turn) return;
        this.deps.log("turn did not end after interrupt; closing the query");
        q.close();
      }, this.deps.interruptGraceMs ?? 30_000);
    }
    try {
      await q.interrupt();
    } catch (err) {
      this.deps.log(`interrupt failed: ${errorText(err)}`);
    }
  }

  private async settings(
    cmd: string,
    mode?: PermissionMode,
    model?: string,
  ): Promise<void> {
    if (mode !== undefined) {
      const before = this.mode;
      this.mode = mode;
      if (this.q !== undefined) {
        try {
          const r = await this.q.setPermissionMode(toSDKMode(mode));
          const now = fromSDKMode(r?.mode);
          if (now !== undefined && now !== mode) {
            this.mode = now;
            await this.emit(
              { kind: "session.settings", data: { mode: now } },
              cmd,
            );
          }
        } catch (err) {
          // The host logged the new mode already: put the one in force back
          // on record.
          this.deps.log(`setPermissionMode failed: ${errorText(err)}`);
          this.mode = before;
          await this.emit(notice("mode_change_failed", "error", { mode }), cmd);
          await this.emit(
            { kind: "session.settings", data: { mode: before } },
            cmd,
          );
        }
      }
    }
    if (model !== undefined) {
      const before = this.model;
      this.model = model;
      if (this.q !== undefined) {
        try {
          await this.q.setModel(model);
        } catch (err) {
          // As for the mode: the host logged the new model already.
          this.deps.log(`setModel failed: ${errorText(err)}`);
          this.model = before;
          await this.emit(
            notice("model_change_failed", "error", { model }),
            cmd,
          );
          if (before !== undefined) {
            await this.emit(
              { kind: "session.settings", data: { model: before } },
              cmd,
            );
          }
        }
      }
    }
  }

  // canUseTool is the SDK's permission callback. Requests are shown to the
  // owner one at a time (the host keeps one open request per session); later
  // ones wait their turn.
  private canUseTool(
    toolName: string,
    input: Record<string, unknown>,
    opts: Parameters<CanUseTool>[2],
  ): Promise<PermissionResult> {
    const { signal, ...rest } = opts;
    this.deps.record?.({
      callback: "canUseTool",
      toolName,
      input,
      options: rest,
    });
    return new Promise<PermissionResult>((resolve) => {
      const req: ToolRequest = {
        requestId: opts.requestId,
        toolName,
        input,
        toolUseID: opts.toolUseID,
        ...(opts.suggestions !== undefined && {
          suggestions: opts.suggestions,
        }),
        ...(opts.suppressAlwaysAllowRule !== undefined && {
          suppressAlwaysAllowRule: opts.suppressAlwaysAllowRule,
        }),
        ...(opts.blockedPath !== undefined && {
          blockedPath: opts.blockedPath,
        }),
      };
      let done = false;
      const p: Pending = {
        req,
        kind: requestKind(toolName),
        shown: false,
        resolve: (r) => {
          if (done) return;
          done = true;
          this.deps.record?.({ reply: req.requestId, result: r });
          resolve(r);
        },
      };
      if (signal.aborted || this.stopping || this.turn?.interrupted) {
        p.resolve({ behavior: "deny", message: INTERRUPTED, interrupt: true });
        return;
      }
      signal.addEventListener("abort", () => this.aborted(p), { once: true });
      this.queue.push(p);
      this.nextRequest();
    });
  }

  // toolStarted notes a tool row that is out; a request held for it follows.
  private toolStarted(toolId: string): void {
    this.startedTools.add(toolId);
    if (this.holding?.req.toolUseID === toolId) this.showRequest();
  }

  private nextRequest(): void {
    if (this.active !== undefined) return;
    const p = this.queue.shift();
    if (p === undefined) return;
    this.active = p;
    // The SDK calls canUseTool as soon as the CLI asks, possibly before the
    // assistant message with the tool call has come through the message
    // stream: the request waits for its tool row, so the timeline reads in
    // order (briefly: a call that never shows up is asked anyway).
    if (this.startedTools.has(p.req.toolUseID)) {
      this.showRequest();
      return;
    }
    this.holding = p;
    this.holdTimer = setTimeout(() => this.showRequest(), 1000);
  }

  private unhold(): void {
    if (this.holdTimer !== undefined) clearTimeout(this.holdTimer);
    this.holdTimer = undefined;
    this.holding = undefined;
  }

  private showRequest(): void {
    this.unhold();
    const p = this.active;
    if (p === undefined || p.shown) return;
    p.shown = true;
    void this.emit(
      requestDraft(p.req, {
        cwd: this.spec.cwd,
        mode: this.mode,
        ...(this.modeCap !== undefined && { modeCap: this.modeCap }),
      }),
    ).then((ok) => {
      if (ok || this.active !== p) return;
      // Too large to show even shortened: the owner can never answer it, so
      // the agent hears no instead of waiting forever.
      this.active = undefined;
      this.answered.add(p.req.requestId);
      p.resolve({ behavior: "deny", message: TOO_LARGE });
      void this.emit(
        notice("request_too_large", "error", {
          requestId: p.req.requestId,
          tool: p.req.toolName,
        }),
      );
      this.nextRequest();
    });
  }

  private settle(
    cmd: string,
    kind: RequestKind,
    requestId: string,
    answer: (req: ToolRequest) => PermissionResult,
  ): void {
    const p = this.active;
    if (
      p === undefined ||
      !p.shown ||
      p.kind !== kind ||
      p.req.requestId !== requestId
    ) {
      const reason = this.answered.has(requestId) ? "answered" : "unknown";
      void this.emit(
        notice("sidecar_reply_not_pending", "warning", { requestId, reason }),
        cmd,
      );
      return;
    }
    this.active = undefined;
    this.answered.add(requestId);
    p.resolve(answer(p.req));
    this.nextRequest();
  }

  // aborted: the SDK withdrew a request (its signal). A shown one is closed
  // as expired, which frees the host's single pending slot, so the next
  // request can follow at once; a late reply for it is acknowledged as
  // answered and not applied.
  private aborted(p: Pending): void {
    const i = this.queue.indexOf(p);
    if (i >= 0) this.queue.splice(i, 1);
    if (this.active === p) {
      this.active = undefined;
      this.unhold();
      this.answered.add(p.req.requestId);
      if (p.shown) void this.emit(expiredDraft(p));
      this.nextRequest();
    }
    p.resolve({ behavior: "deny", message: CANCELLED, interrupt: true });
  }

  // cancelRequests answers every waiting request as denied: on interrupt,
  // shutdown, or a turn that ended without them. The host expires the one
  // it shows when the turn ends.
  private cancelRequests(message: string, interrupt = false): void {
    const all = [
      ...(this.active ? [this.active] : []),
      ...this.queue.splice(0),
    ];
    this.active = undefined;
    this.unhold();
    for (const p of all) {
      this.answered.add(p.req.requestId);
      p.resolve({
        behavior: "deny",
        message,
        ...(interrupt && { interrupt: true }),
      });
    }
  }

  // shutdown ends the session: after the running turn (whenIdle) or now,
  // interrupting it. Resolves when the query is closed and every draft is out.
  // An immediate shutdown (stdin closed, SIGTERM, host.shutdown without
  // whenIdle) also cuts a when-idle one short.
  shutdown(whenIdle: boolean): Promise<void> {
    if (whenIdle) this.draining = true;
    else {
      this.stopping = true;
      this.wakeStop?.();
    }
    if (this.closing !== undefined) return this.closing;
    this.closing = (async () => {
      if (!this.stopping) {
        await Promise.race([
          this.idle(),
          new Promise<void>((r) => (this.wakeStop = r)),
        ]);
      }
      this.stopping = true;
      if (this.turn !== undefined) {
        await this.interrupt();
        let timer: ReturnType<typeof setTimeout> | undefined;
        const grace = new Promise<void>((r) => {
          timer = setTimeout(r, this.deps.interruptGraceMs ?? 30_000);
        });
        await Promise.race([this.idle(), grace]);
        clearTimeout(timer);
      }
      this.cancelRequests(INTERRUPTED, true);
      this.input?.close();
      this.q?.close();
      await this.pump;
      if (this.interruptTimer !== undefined) clearTimeout(this.interruptTimer);
    })();
    return this.closing;
  }
}

// expiredDraft closes a withdrawn request in the contract's terms.
function expiredDraft(p: Pending): EventDraft {
  const requestId = p.req.requestId;
  switch (p.kind) {
    case "question": {
      const data: QuestionResolved = { requestId, outcome: "expired" };
      return { kind: "question.resolved", data };
    }
    case "plan": {
      const data: PlanResolved = { requestId, decision: "expired" };
      return { kind: "plan.resolved", data };
    }
    default: {
      const data: PermissionResolved = { requestId, decision: "expired" };
      return { kind: "permission.resolved", data };
    }
  }
}

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
