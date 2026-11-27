import type {
  FileChange,
  MessageOrigin,
  NoticeLevel,
  PermissionDecision,
  PermissionRequested,
  PlanProposed,
  PlanResolved,
  Provider,
  QuestionAsked,
  QuestionResolved,
  SessionStatus,
  TodoItem,
  ToolKind,
  ToolResult,
  ToolStatus,
  TurnOutcome,
  TypedEvent,
} from "../contract/events.gen";
import type { StreamState } from "./state";

// One tool call: tool.started and, once there, its tool.finished.
export interface ToolRow {
  type: "tool";
  key: string;
  toolId: string;
  name: string;
  kind: ToolKind;
  path?: string;
  command?: string;
  pattern?: string;
  url?: string;
  description?: string;
  // The raw input: all a tool the adapter does not know shows of its call.
  input?: unknown;
  inputTruncated?: boolean;
  // Called by a subagent (parentTool).
  nested: boolean;
  finished?: {
    status: ToolStatus;
    result?: ToolResult;
    durationMs?: number;
    output?: string;
    outputTruncated?: boolean;
    rawResult?: unknown;
    rawResultTruncated?: boolean;
  };
}

// A provider message the adapter does not map (provider.passthrough): its
// name and the message as received. Nothing an agent emits is invisible.
export interface RawRow {
  type: "raw";
  key: string;
  provider: Provider;
  name: string;
  raw: unknown;
  rawTruncated: boolean;
  nested: boolean;
}

// One run of the check queue: gate.queued, gate.started, gate.finished.
export interface GateRow {
  type: "gate";
  key: string;
  repo?: string;
  state: "queued" | "started" | "finished";
  position?: number;
  aheadRepo?: string;
  exitCode?: number;
  durationMs?: number;
}

export type ActivityRow = ToolRow | GateRow | RawRow;

export interface UserItem {
  type: "user";
  key: string;
  text: string;
  ts: string;
  origin: MessageOrigin;
}

// The activity of one turn: its tool and gate rows under "Worked for ...".
export interface FoldItem {
  type: "fold";
  key: string;
  running: boolean;
  // turn.started's time, for the running clock.
  startedAt?: string;
  durationMs?: number;
  outcome?: TurnOutcome;
  rows: ActivityRow[];
}

export interface TextItem {
  type: "text";
  key: string;
  text: string;
  // Still being written (deltas); replaced by the durable assistant.text.
  live: boolean;
}

export interface TodoListItem {
  type: "todo";
  key: string;
  items: TodoItem[];
}

export interface PermissionItem {
  type: "permission";
  key: string;
  request: PermissionRequested;
  decision?: PermissionDecision;
}

// The agent's questions (question.asked) and, once there, how they were
// resolved (question.resolved).
export interface QuestionItem {
  type: "question";
  key: string;
  request: QuestionAsked;
  // The turn it was asked in: a question is only answerable while it runs.
  turn?: string;
  resolved?: QuestionResolved;
  // Still answerable: no resolution and its turn has not ended.
  open: boolean;
}

// A plan the agent presents for approval (plan.proposed) and its outcome
// (plan.resolved).
export interface PlanItem {
  type: "plan";
  key: string;
  request: PlanProposed;
  turn?: string;
  resolved?: PlanResolved;
  open: boolean;
}

// A message waiting in the session's queue (message.queued until its
// message.dequeued).
export interface QueuedMessage {
  messageId: string;
  text: string;
  origin: MessageOrigin;
}

export interface FilesItem {
  type: "files";
  key: string;
  repo: string;
  files: FileChange[];
}

export interface ResumedItem {
  type: "resumed";
  key: string;
  ts: string;
}

export interface NoticeItem {
  type: "notice";
  key: string;
  code: string;
  level: NoticeLevel;
  params?: Record<string, string>;
}

export type TimelineItem =
  | UserItem
  | FoldItem
  | TextItem
  | TodoListItem
  | PermissionItem
  | QuestionItem
  | PlanItem
  | FilesItem
  | ResumedItem
  | NoticeItem;

export interface Folded {
  items: TimelineItem[];
  // The latest session.status in the log.
  status?: SessionStatus;
  // A turn.started without its turn.ended.
  turnOpen: boolean;
  // The tool the open turn runs right now, for the working indicator.
  openTool?: ToolRow;
  provider?: Provider;
  // The queue, head first.
  queue: QueuedMessage[];
  // The question waiting for an answer: unresolved and its turn still
  // running. A message typed in the composer answers it.
  pendingQuestion?: QuestionItem;
}

// open: a request of this turn that has no resolution yet. A request whose
// turn ended (an interrupt, a lost turn) can no longer be answered even
// without its resolved event.
export function stillOpen(
  item: { turn?: string; resolved?: unknown },
  ended: ReadonlySet<string>,
): boolean {
  if (item.resolved !== undefined) return false;
  return item.turn === undefined || !ended.has(item.turn);
}

// foldEvents turns a session's stream into timeline items. Pure: the same
// state gives the same items, with keys stable across calls (turn, toolId,
// requestId, blockId or seq), so React keeps each row's local state.
export function foldEvents(state: StreamState): Folded {
  const items: TimelineItem[] = [];
  const folds = new Map<string, FoldItem>();
  const tools = new Map<string, ToolRow>();
  const toolOrder: { row: ToolRow; fold: FoldItem }[] = [];
  const perms = new Map<string, PermissionItem>();
  const todos = new Map<string, TodoListItem>();
  const texts = new Map<string, TextItem>();
  const gates: GateRow[] = [];
  const questions = new Map<string, QuestionItem>();
  const plans = new Map<string, PlanItem>();
  const ended = new Set<string>();
  // The queue by message id, in insertion order; the head is moved to the
  // front by rebuilding.
  let queue: QueuedMessage[] = [];
  const out: Folded = { items, turnOpen: false, queue };

  // foldFor finds the fold of the event's turn, created where the turn's
  // first event sits. Events outside a turn share a fold with the turnless
  // rows right before them.
  const foldFor = (e: TypedEvent): FoldItem => {
    if (e.turn !== undefined) {
      let f = folds.get(e.turn);
      if (!f) {
        f = { type: "fold", key: `turn:${e.turn}`, running: false, rows: [] };
        folds.set(e.turn, f);
        items.push(f);
      }
      return f;
    }
    const last = items[items.length - 1];
    if (last?.type === "fold" && last.key.startsWith("fold:")) return last;
    const f: FoldItem = {
      type: "fold",
      key: `fold:${e.seq ?? items.length}`,
      running: false,
      rows: [],
    };
    items.push(f);
    return f;
  };

  // openGate is the latest gate row of repo that has not finished.
  const openGate = (repo?: string): GateRow | undefined => {
    for (let i = gates.length - 1; i >= 0; i--) {
      const g = gates[i];
      if (g.repo === repo && g.state !== "finished") return g;
    }
    return undefined;
  };
  const addGate = (e: TypedEvent, repo?: string): GateRow => {
    const g: GateRow = {
      type: "gate",
      key: `gate:${e.seq}`,
      repo,
      state: "queued",
    };
    gates.push(g);
    foldFor(e).rows.push(g);
    return g;
  };

  for (const e of state.events) {
    switch (e.kind) {
      case "session.started":
        out.provider = e.data.provider;
        break;
      case "session.status":
        out.status = e.data.status;
        break;
      case "message.queued": {
        // The queue holds each id once, where its last message.queued put
        // it: at the head with front, else at the tail.
        const m: QueuedMessage = {
          messageId: e.data.messageId,
          text: e.data.text,
          origin: e.data.origin,
        };
        const rest = queue.filter((q) => q.messageId !== m.messageId);
        queue = e.data.front ? [m, ...rest] : [...rest, m];
        break;
      }
      case "message.dequeued":
        queue = queue.filter((q) => q.messageId !== e.data.messageId);
        break;
      case "message.user":
        // Delivered: no longer waiting, whatever the dequeue said.
        queue = queue.filter((q) => q.messageId !== e.data.messageId);
        items.push({
          type: "user",
          key: `msg:${e.data.messageId}:${e.seq}`,
          text: e.data.text,
          ts: e.ts,
          origin: e.data.origin,
        });
        break;
      case "turn.started": {
        const f = foldFor(e);
        f.running = true;
        f.startedAt = e.ts;
        out.turnOpen = true;
        break;
      }
      case "turn.ended": {
        const f = foldFor(e);
        f.running = false;
        f.durationMs = e.data.durationMs;
        f.outcome = e.data.outcome;
        out.turnOpen = false;
        if (e.turn !== undefined) ended.add(e.turn);
        break;
      }
      case "assistant.text": {
        const prev = texts.get(e.data.blockId);
        if (prev) {
          prev.text = e.data.text;
          break;
        }
        const t: TextItem = {
          type: "text",
          key: `text:${e.data.blockId}`,
          text: e.data.text,
          live: false,
        };
        texts.set(e.data.blockId, t);
        items.push(t);
        break;
      }
      case "tool.started": {
        if (tools.has(e.data.toolId)) break;
        const d = e.data;
        const row: ToolRow = {
          type: "tool",
          key: `tool:${d.toolId}`,
          toolId: d.toolId,
          name: d.name,
          kind: d.kind,
          path: d.path,
          command: d.command,
          pattern: d.pattern,
          url: d.url,
          description: d.description,
          input: d.input,
          inputTruncated: d.inputTruncated,
          nested: e.parentTool !== undefined,
        };
        const f = foldFor(e);
        f.rows.push(row);
        tools.set(d.toolId, row);
        toolOrder.push({ row, fold: f });
        break;
      }
      case "tool.finished": {
        const row = tools.get(e.data.toolId);
        if (row) {
          row.finished = {
            status: e.data.status,
            result: e.data.result,
            durationMs: e.data.durationMs,
            output: e.data.output,
            outputTruncated: e.data.outputTruncated,
            rawResult: e.data.rawResult,
            rawResultTruncated: e.data.rawResultTruncated,
          };
        }
        break;
      }
      case "provider.passthrough": {
        const f = foldFor(e);
        f.rows.push({
          type: "raw",
          key: `raw:${e.seq ?? `${f.key}:${f.rows.length}`}`,
          provider: e.data.provider,
          name: e.data.name,
          raw: e.data.raw,
          rawTruncated: e.data.rawTruncated === true,
          nested: e.parentTool !== undefined,
        });
        break;
      }
      case "permission.requested": {
        if (perms.has(e.data.requestId)) break;
        const p: PermissionItem = {
          type: "permission",
          key: `perm:${e.data.requestId}`,
          request: e.data,
        };
        perms.set(e.data.requestId, p);
        items.push(p);
        break;
      }
      case "permission.resolved": {
        const p = perms.get(e.data.requestId);
        if (p) p.decision = e.data.decision;
        break;
      }
      case "question.asked": {
        if (questions.has(e.data.requestId)) break;
        const q: QuestionItem = {
          type: "question",
          key: `question:${e.data.requestId}`,
          request: e.data,
          turn: e.turn,
          open: true,
        };
        questions.set(e.data.requestId, q);
        items.push(q);
        break;
      }
      case "question.resolved": {
        const q = questions.get(e.data.requestId);
        if (q) q.resolved = e.data;
        break;
      }
      case "plan.proposed": {
        if (plans.has(e.data.requestId)) break;
        const p: PlanItem = {
          type: "plan",
          key: `plan:${e.data.requestId}`,
          request: e.data,
          turn: e.turn,
          open: true,
        };
        plans.set(e.data.requestId, p);
        items.push(p);
        break;
      }
      case "plan.resolved": {
        const p = plans.get(e.data.requestId);
        if (p) p.resolved = e.data;
        break;
      }
      case "todo.updated": {
        const k = e.turn ?? "";
        const prev = todos.get(k);
        if (prev) {
          prev.items = e.data.items;
          break;
        }
        const t: TodoListItem = {
          type: "todo",
          key: `todo:${e.turn ?? e.seq}`,
          items: e.data.items,
        };
        todos.set(k, t);
        items.push(t);
        break;
      }
      case "files.changed":
        items.push({
          type: "files",
          key: `files:${e.seq}`,
          repo: e.data.repo,
          files: e.data.files,
        });
        break;
      case "session.resumed":
        items.push({ type: "resumed", key: `resumed:${e.seq}`, ts: e.ts });
        break;
      case "notice":
        // provider_session is the host's marker that the provider has the
        // session (docs/architecture.md, "Claude sidecar"), nothing to read.
        if (e.data.code === "provider_session") break;
        items.push({
          type: "notice",
          key: `notice:${e.seq}`,
          code: e.data.code,
          level: e.data.level,
          params: e.data.params,
        });
        break;
      case "gate.queued": {
        const g = openGate(e.data.repo) ?? addGate(e, e.data.repo);
        g.state = "queued";
        g.position = e.data.position;
        g.aheadRepo = e.data.aheadRepo;
        break;
      }
      case "gate.started": {
        const g = openGate(e.data.repo) ?? addGate(e, e.data.repo);
        g.state = "started";
        break;
      }
      case "gate.finished": {
        const g = openGate(e.data.repo) ?? addGate(e, e.data.repo);
        g.state = "finished";
        g.exitCode = e.data.exitCode;
        g.durationMs = e.data.durationMs;
        break;
      }
      default:
        // No row: settings, worktrees, thinking, started sessions, pull
        // requests (later areas).
        break;
    }
  }

  out.queue = queue;
  for (const q of questions.values()) {
    q.open = stillOpen(q, ended);
    if (q.open) out.pendingQuestion = q;
  }
  for (const p of plans.values()) p.open = stillOpen(p, ended);

  for (const b of state.live) {
    if (texts.has(b.blockId)) continue;
    items.push({
      type: "text",
      key: `text:${b.blockId}`,
      text: b.text,
      live: true,
    });
  }

  for (let i = toolOrder.length - 1; i >= 0; i--) {
    const { row, fold } = toolOrder[i];
    if (fold.running && !row.finished) {
      out.openTool = row;
      break;
    }
  }
  return out;
}
