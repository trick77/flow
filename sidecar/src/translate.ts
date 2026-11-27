// Translator turns Claude Agent SDK messages into Flow event drafts
// (docs/events.md, "Claude Agent SDK → Flow"). It holds only what the mapping
// needs across messages: the open stream message, block counts per message,
// open tool calls, the todo list and the cost totals the SDK reports
// cumulatively. Every id it writes is a provider id (msg_…, toolu_…) or
// derived from one, never a counter of this process.

import type {
  ModelUsage,
  SDKAssistantMessage,
  SDKMessage,
  SDKPartialAssistantMessage,
  SDKResultMessage,
  SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk";

import type {
  ClaudeAuth,
  EventDraft,
  EventKind,
  EventPayloads,
  Notice,
  PermissionMode,
  ToolFinished,
  ToolStarted,
  ToolStatus,
  TurnEnded,
  Usage,
} from "./contract/events.gen.js";
import { capJSON, cutHeadTail } from "./limits.js";
import { fromSDKMode } from "./modes.js";
import {
  hasStructuredResult,
  resultFacts,
  TodoList,
  toolFields,
  toolKind,
} from "./tools.js";

// PASSTHROUGH is the contract's generic event for anything not mapped.
const PASSTHROUGH = "provider.passthrough" satisfies EventKind;

// Turn is what the session knows about the turn it runs.
export interface Turn {
  // uuid went out on the SDKUserMessage; the result echoes it.
  uuid: string;
  interrupted: boolean;
  // Tool calls the owner denied through the permission callback.
  denied: Set<string>;
  // The last assistant error code seen in the turn.
  lastError?: string;
}

export interface TranslatorConfig {
  cwd: string;
  claudeAuth?: ClaudeAuth;
  // modeCap is the session's cap now (settings.set can change it).
  modeCap?: () => PermissionMode | undefined;
  // mode is the session's Flow mode now.
  mode: () => PermissionMode;
  // resumed: the session ran before this process, so the todo list of the
  // Task tools is not known until Claude Code lists it.
  resumed?: boolean;
  // priorCostUsd is the SDK's cumulative cost before this process: 0 on a
  // first start; on a resume the host's sum of earlier turns, or undefined
  // when unknown (the first turn after it then reports no cost).
  priorCostUsd?: number;
  now?: () => number;
}

export interface Translation {
  drafts: EventDraft[];
  // endsTurn: a result that closes the given turn.
  endsTurn: boolean;
}

interface OpenTool {
  name: string;
  input: Record<string, unknown>;
  startedAt: number;
  parentTool?: string;
}

type Block = { type: string } & Record<string, unknown>;

function draft<K extends EventKind>(
  kind: K,
  data: EventPayloads[K],
  parentTool?: string | null,
): EventDraft {
  const d: EventDraft = { kind, data };
  if (parentTool) d.parentTool = parentTool;
  return d;
}

// messageName is the provider's name for a message: type, or type/subtype.
export function messageName(m: { type: string; subtype?: unknown }): string {
  return typeof m.subtype === "string" ? `${m.type}/${m.subtype}` : m.type;
}

export function passthrough(
  name: string,
  raw: unknown,
  parentTool?: string | null,
): EventDraft {
  const c = capJSON(raw);
  const data: EventPayloads[typeof PASSTHROUGH] = {
    provider: "claude",
    name,
    raw: c.value,
  };
  if (c.truncated) data.rawTruncated = true;
  return draft(PASSTHROUGH, data, parentTool);
}

export function notice(
  code: string,
  level: Notice["level"],
  params?: Record<string, string | number | boolean | null | undefined>,
): EventDraft {
  const data: Notice = { code, level };
  if (params !== undefined) {
    const p: Record<string, string> = {};
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== null) p[k] = String(v);
    }
    if (Object.keys(p).length > 0) data.params = p;
  }
  return draft("notice", data);
}

// Stream events whose content the durable assistant message repeats: the
// deltas of blocks other than text, block and message framing.
const FRAMING_STREAM_EVENTS = new Set([
  "message_start",
  "content_block_start",
  "content_block_delta",
  "content_block_stop",
  "message_delta",
  "message_stop",
  "ping",
]);

// Assistant message errors (SDKAssistantMessageError) as notice codes.
function assistantErrorNotice(error: string): EventDraft {
  switch (error) {
    case "rate_limit":
      return notice("rate_limited", "warning", { error });
    case "authentication_failed":
    case "oauth_org_not_allowed":
    case "cloud_credential_error":
      return notice("auth_required", "error", { error });
    default:
      return notice("provider_error", "error", { error });
  }
}

const RESULT_ERRORS: Record<string, string> = {
  error_max_turns: "max_turns",
  error_max_budget_usd: "budget",
  error_max_structured_output_retries: "structured_output_retries",
};

function round(n: number): number {
  return Math.round(n * 1e9) / 1e9;
}

// COST_TOLERANCE is how far a cumulative total may lie below the last one and
// still count as no spend rather than a reset (USD; totals are rounded to
// 1e-9, so a millionth of a dollar is well above their noise).
const COST_TOLERANCE = 1e-6;

export class Translator {
  private readonly cfg: TranslatorConfig;
  private readonly now: () => number;
  // The open stream message per stream: the main thread ("") and each
  // subagent (its parent tool id) stream separately.
  private readonly streamMessages = new Map<string, string>();
  private readonly blockCounts = new Map<string, number>();
  private readonly openTools = new Map<string, OpenTool>();
  private readonly todos: TodoList;
  private costTotal: number | undefined;
  private modelTotals: Record<string, ModelUsage> | undefined;
  // echoes: Claude Code stamps frames with the uuid of the user message
  // they answer (user_message_uuid); once seen, a result without the stamp
  // belongs to a turn it started itself.
  private echoes = false;
  // model is the session model the last init reported.
  model: string | undefined;

  constructor(cfg: TranslatorConfig) {
    this.cfg = cfg;
    this.now = cfg.now ?? Date.now;
    this.todos = new TodoList(cfg.resumed !== true);
    this.costTotal = cfg.priorCostUsd;
    this.modelTotals = cfg.priorCostUsd === 0 ? {} : undefined;
  }

  // seedStreamMessage sets the open stream message as a message_start would
  // (tests that start mid-stream).
  seedStreamMessage(id: string, parentTool = ""): void {
    this.streamMessages.set(parentTool, id);
  }

  translate(m: SDKMessage, turn?: Turn): Translation {
    const echo = m as {
      user_message_uuid?: unknown;
      user_message_uuids?: unknown;
    };
    if (
      typeof echo.user_message_uuid === "string" ||
      Array.isArray(echo.user_message_uuids)
    ) {
      this.echoes = true;
    }
    switch (m.type) {
      case "assistant":
        return { drafts: this.assistant(m, turn), endsTurn: false };
      case "user":
        return { drafts: this.user(m, turn), endsTurn: false };
      case "stream_event":
        return { drafts: this.stream(m), endsTurn: false };
      case "result":
        return this.result(m, turn);
      case "tool_progress":
        return {
          drafts: [
            draft(
              "tool.progress",
              {
                toolId: m.tool_use_id,
                elapsedMs: Math.round(m.elapsed_time_seconds * 1000),
              },
              m.parent_tool_use_id,
            ),
          ],
          endsTurn: false,
        };
      case "rate_limit_event": {
        const i = m.rate_limit_info;
        const params = {
          rateLimitType: i.rateLimitType,
          resetsAt: i.resetsAt,
          utilization: i.utilization,
        };
        if (i.status === "rejected") {
          return {
            drafts: [notice("rate_limited", "error", params)],
            endsTurn: false,
          };
        }
        if (i.status === "allowed_warning") {
          return {
            drafts: [notice("rate_limit_warning", "warning", params)],
            endsTurn: false,
          };
        }
        break;
      }
      case "auth_status":
        if (m.error !== undefined) {
          return {
            drafts: [notice("auth_required", "error", { error: m.error })],
            endsTurn: false,
          };
        }
        break;
      case "system":
        switch (m.subtype) {
          case "init":
            this.model = m.model;
            return {
              drafts: [
                draft("session.started", {
                  provider: "claude",
                  ...(this.cfg.claudeAuth !== undefined && {
                    claudeAuth: this.cfg.claudeAuth,
                  }),
                  model: m.model,
                  mode: fromSDKMode(m.permissionMode) ?? this.cfg.mode(),
                  ...(this.cfg.modeCap?.() !== undefined && {
                    modeCap: this.cfg.modeCap(),
                  }),
                  providerSessionId: m.session_id,
                  cwd: m.cwd,
                }),
              ],
              endsTurn: false,
            };
          case "compact_boundary": {
            const c = m.compact_metadata;
            return {
              drafts: [
                notice("context_compacted", "info", {
                  trigger: c.trigger,
                  preTokens: c.pre_tokens,
                  postTokens: c.post_tokens,
                }),
              ],
              endsTurn: false,
            };
          }
          case "api_retry":
            return {
              drafts: [
                notice("api_retry", "warning", {
                  attempt: m.attempt,
                  maxRetries: m.max_retries,
                  retryDelayMs: m.retry_delay_ms,
                  errorStatus: m.error_status,
                  error: m.error,
                }),
              ],
              endsTurn: false,
            };
          case "status": {
            const drafts: EventDraft[] = [];
            const mode = fromSDKMode(m.permissionMode);
            if (mode !== undefined && mode !== this.cfg.mode()) {
              drafts.push(draft("session.settings", { mode }));
            }
            if (m.compact_result === "failed") {
              drafts.push(
                notice("context_compaction_failed", "warning", {
                  error: m.compact_error,
                }),
              );
            }
            // A bare status (compacting, requesting, null) is the CLI's own
            // activity indicator: Flow shows the turn as working already.
            return { drafts, endsTurn: false };
          }
          case "task_progress":
            if (m.tool_use_id !== undefined) {
              return {
                drafts: [
                  draft("tool.progress", {
                    toolId: m.tool_use_id,
                    elapsedMs: m.usage.duration_ms,
                  }),
                ],
                endsTurn: false,
              };
            }
            break;
          case "task_started":
          case "task_notification":
            // Subagent and background task bookends: shown generically on the
            // tool row they belong to until Flow maps them.
            return {
              drafts: [passthrough(messageName(m), m, m.tool_use_id)],
              endsTurn: false,
            };
        }
        break;
    }
    return { drafts: [passthrough(messageName(m), m)], endsTurn: false };
  }

  private stream(m: SDKPartialAssistantMessage): EventDraft[] {
    const e = m.event as { type: string } & Record<string, unknown>;
    const stream = m.parent_tool_use_id ?? "";
    if (e.type === "message_start") {
      const msg = e.message as { id?: unknown } | undefined;
      if (typeof msg?.id === "string") this.streamMessages.set(stream, msg.id);
      return [];
    }
    if (e.type === "content_block_delta") {
      const delta = e.delta as { type?: unknown; text?: unknown } | undefined;
      if (delta?.type === "text_delta" && typeof delta.text === "string") {
        const block = `${this.streamMessages.get(stream) ?? m.uuid}:${String(e.index)}`;
        return [
          draft(
            "assistant.text_delta",
            { blockId: block, text: delta.text },
            m.parent_tool_use_id,
          ),
        ];
      }
      return [];
    }
    if (e.type === "message_stop") this.streamMessages.delete(stream);
    if (FRAMING_STREAM_EVENTS.has(e.type)) return [];
    return [passthrough(`stream_event/${e.type}`, m, m.parent_tool_use_id)];
  }

  private assistant(m: SDKAssistantMessage, turn?: Turn): EventDraft[] {
    const out: EventDraft[] = [];
    const parent = m.parent_tool_use_id;
    const msg = m.message;
    const content = (Array.isArray(msg.content)
      ? msg.content
      : []) as unknown as Block[];
    // While a response streams the CLI sends one assistant message per
    // finished block, all with the same message id: the block index is the
    // number of blocks of that message seen before, as in the stream events.
    const base = this.blockCounts.get(msg.id) ?? 0;
    this.blockCounts.set(msg.id, base + content.length);
    content.forEach((b, i) => {
      const blockId = `${msg.id}:${base + i}`;
      switch (b.type) {
        case "text":
          out.push(
            draft(
              "assistant.text",
              { blockId, text: String(b.text ?? "") },
              parent,
            ),
          );
          return;
        case "thinking":
          out.push(
            draft(
              "assistant.thinking",
              { blockId, text: String(b.thinking ?? "") },
              parent,
            ),
          );
          return;
        case "tool_use":
        case "server_tool_use":
        case "mcp_tool_use":
          out.push(...this.toolStarted(b, parent));
          return;
      }
      if (
        b.type.endsWith("_tool_result") &&
        typeof b.tool_use_id === "string"
      ) {
        // A server tool's result inside the assistant message: its blocks
        // (search results, fetched pages) are structured, kept as rawResult.
        const failed =
          typeof (b.content as { type?: unknown } | null)?.type === "string" &&
          String((b.content as { type: string }).type).endsWith("_error");
        const fin = this.toolFinished(
          b.tool_use_id,
          b.content,
          undefined,
          failed,
          parent,
          turn,
        );
        const data = fin[0].data as ToolFinished;
        if (b.content !== undefined && data.rawResult === undefined) {
          const c = capJSON(b.content);
          data.rawResult = c.value;
          if (c.truncated) data.rawResultTruncated = true;
        }
        out.push(...fin);
        return;
      }
      out.push(passthrough(`assistant/${b.type}`, b, parent));
    });
    if (m.error !== undefined) {
      if (turn !== undefined) turn.lastError = m.error;
      out.push(assistantErrorNotice(m.error));
    }
    return out;
  }

  private toolStarted(b: Block, parent: string | null): EventDraft[] {
    const id = String(b.id);
    const name = String(b.name);
    const input =
      b.input !== null && typeof b.input === "object"
        ? (b.input as Record<string, unknown>)
        : {};
    const kind = toolKind(name);
    const data: ToolStarted = {
      toolId: id,
      name,
      kind,
      ...toolFields(name, input, this.cfg.cwd),
      // The whole input: fitDraft shortens it only when the line would
      // exceed the host's limit (inputTruncated).
      input: b.input ?? {},
    };
    this.openTools.set(id, {
      name,
      input,
      startedAt: this.now(),
      ...(parent ? { parentTool: parent } : {}),
    });
    const out = [draft("tool.started", data, parent)];
    if (name === "TodoWrite" && this.todos.fromTodoWrite(input)) {
      out.push(draft("todo.updated", { items: this.todos.snapshot() }, parent));
    } else {
      this.todos.started(id, name, input);
    }
    return out;
  }

  private user(m: SDKUserMessage, turn?: Turn): EventDraft[] {
    const content = m.message.content;
    const blocks = (Array.isArray(content)
      ? content
      : []) as unknown as Block[];
    const results = blocks.filter((b) => b.type === "tool_result");
    if (results.length === 0) {
      // The owner's own prompt echoed back is already logged as message.user.
      if ("isReplay" in m && m.isReplay === true && m.uuid === turn?.uuid)
        return [];
      return [passthrough("user", m, m.parent_tool_use_id)];
    }
    const out: EventDraft[] = [];
    // tool_use_result is the structured output of the message's tool call;
    // it is only unambiguous for a message with one result.
    const structured = results.length === 1 ? m.tool_use_result : undefined;
    for (const b of results) {
      out.push(
        ...this.toolFinished(
          String(b.tool_use_id),
          b.content,
          structured,
          b.is_error === true,
          m.parent_tool_use_id,
          turn,
        ),
      );
    }
    const rest = blocks.filter((b) => b.type !== "tool_result");
    if (rest.length > 0) {
      out.push(
        passthrough(
          "user",
          { ...m, message: { ...m.message, content: rest } },
          m.parent_tool_use_id,
        ),
      );
    }
    return out;
  }

  private toolFinished(
    id: string,
    content: unknown,
    structured: unknown,
    isError: boolean,
    parent: string | null,
    turn?: Turn,
  ): EventDraft[] {
    const open = this.openTools.get(id);
    this.openTools.delete(id);
    let status: ToolStatus = "ok";
    if (turn?.denied.has(id)) status = "denied";
    else if (isError) status = turn?.interrupted ? "interrupted" : "error";
    else if (
      (structured as { interrupted?: unknown } | undefined)?.interrupted ===
      true
    ) {
      status = "interrupted";
    }
    const data: ToolFinished = { toolId: id, status };
    const text = outputText(content);
    if (text !== "") {
      const o = cutHeadTail(text);
      data.output = o.text;
      if (o.truncated) data.outputTruncated = true;
    }
    if (open !== undefined && hasStructuredResult(toolKind(open.name))) {
      const facts = resultFacts(open.name, structured);
      if (facts !== undefined) data.result = facts;
    } else if (structured !== null && typeof structured === "object") {
      const c = capJSON(structured);
      data.rawResult = c.value;
      if (c.truncated) data.rawResultTruncated = true;
    }
    if (open !== undefined)
      data.durationMs = Math.max(0, Math.round(this.now() - open.startedAt));
    const out = [draft("tool.finished", data, parent ?? open?.parentTool)];
    if (
      open !== undefined &&
      this.todos.finished(
        id,
        open.name,
        open.input,
        structured,
        status === "ok",
      )
    ) {
      out.push(
        draft(
          "todo.updated",
          { items: this.todos.snapshot() },
          parent ?? open.parentTool,
        ),
      );
    }
    return out;
  }

  // closeOpenTools ends every tool call still open as interrupted: the turn
  // ended without their results.
  closeOpenTools(): EventDraft[] {
    const out: EventDraft[] = [];
    for (const [id, t] of this.openTools) {
      out.push(
        draft(
          "tool.finished",
          {
            toolId: id,
            status: "interrupted",
            durationMs: Math.max(0, Math.round(this.now() - t.startedAt)),
          },
          t.parentTool,
        ),
      );
    }
    this.openTools.clear();
    return out;
  }

  // belongs: the result answers the turn's message. The SDK stamps a
  // result with the uuids of the user messages it consumed; it leaves the
  // stamp out on a turn without a client uuid (one Claude Code started
  // itself), on a crashed worker's zeroed result, and in older CLIs. So an
  // unstamped result is the running turn's only when it is zeroed or this
  // CLI has never stamped a frame.
  private belongs(m: SDKResultMessage, turn: Turn): boolean {
    const uuids =
      m.user_message_uuids ??
      (m.user_message_uuid ? [m.user_message_uuid] : []);
    if (uuids.length > 0) return uuids.includes(turn.uuid);
    return zeroed(m) || !this.echoes;
  }

  private result(m: SDKResultMessage, turn?: Turn): Translation {
    if (turn === undefined || !this.belongs(m, turn)) {
      // A turn Claude Code started on its own (a background task's
      // notification): no message.send to end, so its result is shown as is.
      // The totals stay where they are: its spend is counted in the next
      // turn that ends, the only place the contract has for it.
      return { drafts: [passthrough("result", m)], endsTurn: false };
    }
    // A zeroed result (crash, startup failure) says nothing about the spend:
    // the totals stay, and the turn reports no cost.
    const cost = zeroed(m) ? undefined : this.costDelta(m.total_cost_usd);
    const model = zeroed(m) ? undefined : this.turnModel(m.modelUsage);
    const t: TurnEnded = { outcome: "completed", durationMs: m.duration_ms };
    if (m.subtype === "success") {
      if (m.is_error) {
        t.outcome = "error";
        t.errorCode = turn.lastError ?? "provider_error";
      }
    } else if (m.subtype === "error_during_execution") {
      if (turn.interrupted) t.outcome = "interrupted";
      else {
        t.outcome = "error";
        t.errorCode = turn.lastError ?? "execution";
      }
    } else {
      t.outcome = "error";
      t.errorCode = RESULT_ERRORS[m.subtype] ?? String(m.subtype);
    }
    const usage = mapUsage(m.usage);
    if (usage !== undefined) t.usage = usage;
    if (cost !== undefined) t.costUsd = cost;
    if (model !== undefined) {
      t.provider = "claude";
      t.model = model;
    }
    return {
      drafts: [...this.closeOpenTools(), draft("turn.ended", t)],
      endsTurn: true,
    };
  }

  // costDelta is this result's share of the SDK's cumulative total_cost_usd.
  // Only a clear drop below the last total is a reset (/clear) and counts from
  // zero: a total a hair below it (the host's rounded sum after a resume, or
  // float noise) is no spend, never the whole session billed again.
  private costDelta(total: unknown): number | undefined {
    if (typeof total !== "number") return undefined;
    const prev = this.costTotal;
    this.costTotal = total;
    if (prev === undefined) return undefined;
    if (total < prev - COST_TOLERANCE) return round(total);
    return round(Math.max(0, total - prev));
  }

  // turnModel is the model that carried most of this result's cost, from the
  // cumulative modelUsage; without an earlier snapshot the totals stand for
  // the turn.
  private turnModel(
    usage: Record<string, ModelUsage> | undefined,
  ): string | undefined {
    if (usage === null || typeof usage !== "object") return undefined;
    const prev = this.modelTotals ?? {};
    this.modelTotals = usage;
    let best: string | undefined;
    let bestCost = -1;
    let bestOut = -1;
    for (const [name, u] of Object.entries(usage)) {
      const p = prev[name];
      const cost = (u.costUSD ?? 0) - (p?.costUSD ?? 0);
      const out = (u.outputTokens ?? 0) - (p?.outputTokens ?? 0);
      if (cost > bestCost || (cost === bestCost && out > bestOut)) {
        best = name;
        bestCost = cost;
        bestOut = out;
      }
    }
    return bestCost > 0 || bestOut > 0 ? best : undefined;
  }
}

// zeroed: an error result that carries no totals (the SDK types: "crash /
// startup-error results may carry zeroed values").
function zeroed(m: SDKResultMessage): boolean {
  if (m.subtype === "success" && !m.is_error) return false;
  if ("startup_failure_reason" in m && m.startup_failure_reason !== undefined)
    return true;
  const models = Object.values(m.modelUsage ?? {});
  return m.total_cost_usd === 0 && models.every((u) => (u.costUSD ?? 0) === 0);
}

function mapUsage(u: unknown): Usage | undefined {
  if (u === null || typeof u !== "object") return undefined;
  const o = u as Record<string, unknown>;
  const n = (v: unknown) =>
    typeof v === "number" && Number.isFinite(v) ? v : undefined;
  const usage: Usage = {
    inputTokens: n(o.input_tokens) ?? 0,
    outputTokens: n(o.output_tokens) ?? 0,
  };
  const read = n(o.cache_read_input_tokens);
  if (read !== undefined) usage.cacheReadTokens = read;
  const write = n(o.cache_creation_input_tokens);
  if (write !== undefined) usage.cacheWriteTokens = write;
  return usage;
}

// outputText is a tool_result's content as text: a string, or its text
// blocks; other blocks (images, documents) by their type.
function outputText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content))
    return content === undefined ? "" : JSON.stringify(content);
  return content
    .map((c: unknown) => {
      const b = c as { type?: unknown; text?: unknown };
      if (b?.type === "text" && typeof b.text === "string") return b.text;
      return `[${String(b?.type ?? "content")}]`;
    })
    .join("\n");
}
