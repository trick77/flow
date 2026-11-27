// Claude Code tools as Flow sees them: the tool row's kind and summary
// fields, the permission card's kind, the result facts, and the todo list the
// sidecar keeps for both tool generations (docs/events.md, "Claude Agent SDK
// → Flow"). Input and output shapes follow the SDK's sdk-tools.d.ts.

import { isAbsolute, relative } from "node:path";

import type {
  PermissionKind,
  TodoItem,
  TodoStatus,
  ToolKind,
  ToolResult,
  ToolStarted,
} from "./contract/events.gen.js";

const KINDS: Record<string, ToolKind> = {
  Read: "read",
  Edit: "edit",
  MultiEdit: "edit",
  NotebookEdit: "edit",
  Write: "write",
  Grep: "search",
  Glob: "search",
  Bash: "bash",
  WebFetch: "web",
  WebSearch: "web",
  web_search: "web",
  web_fetch: "web",
  TodoWrite: "todo",
  TaskCreate: "todo",
  TaskUpdate: "todo",
  TaskGet: "todo",
  TaskList: "todo",
  // The subagent tool is Agent; Claude Code before 2.1.63 named it Task
  // (subagents page).
  Agent: "subagent",
  Task: "subagent",
  AskUserQuestion: "question",
  ExitPlanMode: "plan",
};

export function toolKind(name: string): ToolKind {
  if (name.startsWith("mcp__")) return "mcp";
  return KINDS[name] ?? "other";
}

export function permissionKind(name: string): PermissionKind {
  switch (toolKind(name)) {
    case "bash":
      return "command";
    case "edit":
      return "edit";
    case "write":
      return "write";
    case "read":
    case "search":
      return "read";
    case "web":
      return "web";
    case "mcp":
      return "mcp";
    default:
      return "other";
  }
}

type Input = Record<string, unknown>;

function str(v: unknown): string | undefined {
  return typeof v === "string" && v !== "" ? v : undefined;
}

// displayPath is a path relative to the session directory when it lies inside
// it, else as given.
export function displayPath(p: string, cwd: string): string {
  if (!isAbsolute(p) || cwd === "") return p;
  const rel = relative(cwd, p);
  if (rel === "" || rel.startsWith("..") || isAbsolute(rel)) return p;
  return rel;
}

// toolFields are the summary fields of a tool row taken from its input.
export function toolFields(
  name: string,
  input: Input,
  cwd: string,
): Pick<ToolStarted, "path" | "command" | "pattern" | "url" | "description"> {
  const out: Pick<
    ToolStarted,
    "path" | "command" | "pattern" | "url" | "description"
  > = {};
  const path =
    str(input.file_path) ?? str(input.notebook_path) ?? str(input.path);
  if (path !== undefined) out.path = displayPath(path, cwd);
  const kind = toolKind(name);
  if (kind === "bash") {
    const command = str(input.command);
    if (command !== undefined) out.command = command;
  }
  if (kind === "search") {
    const pattern = str(input.pattern);
    if (pattern !== undefined) out.pattern = pattern;
  }
  if (kind === "web") {
    const url = str(input.url);
    if (url !== undefined) out.url = url;
  }
  if (kind === "bash" || kind === "subagent") {
    const description = str(input.description);
    if (description !== undefined) out.description = description;
  }
  return out;
}

// Kinds whose structured result the sidecar reads into ToolResult facts; for
// every other tool (unknown, MCP) the structured result travels as rawResult.
const STRUCTURED: ReadonlySet<ToolKind> = new Set([
  "read",
  "edit",
  "write",
  "search",
  "bash",
  "web",
  "todo",
  "subagent",
  "question",
  "plan",
]);

export function hasStructuredResult(kind: ToolKind): boolean {
  return STRUCTURED.has(kind);
}

function obj(v: unknown): Record<string, unknown> | undefined {
  return v !== null && typeof v === "object" && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : undefined;
}

function num(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

// patchCounts sums added and removed lines of a structuredPatch
// (FileEditOutput, FileWriteOutput).
function patchCounts(
  patch: unknown,
): { added: number; removed: number } | undefined {
  if (!Array.isArray(patch)) return undefined;
  let added = 0;
  let removed = 0;
  for (const hunk of patch) {
    const lines = obj(hunk)?.lines;
    if (!Array.isArray(lines)) continue;
    for (const l of lines) {
      if (typeof l !== "string") continue;
      if (l.startsWith("+")) added++;
      else if (l.startsWith("-")) removed++;
    }
  }
  return { added, removed };
}

// resultFacts reads the facts of a tool row from the tool's structured
// output (the user message's tool_use_result).
export function resultFacts(
  name: string,
  output: unknown,
): ToolResult | undefined {
  const o = obj(output);
  if (o === undefined) return undefined;
  const r: ToolResult = {};
  switch (toolKind(name)) {
    case "edit": {
      const c = patchCounts(o.structuredPatch);
      if (c !== undefined) Object.assign(r, c);
      break;
    }
    case "write": {
      if (o.type === "create") r.created = true;
      else if (o.type === "update") r.created = false;
      // A new file's structuredPatch may be empty: count its content instead.
      const c = patchCounts(o.structuredPatch);
      if (c !== undefined && c.added + c.removed > 0) Object.assign(r, c);
      else if (o.type === "create" && typeof o.content === "string") {
        r.added = o.content === "" ? 0 : o.content.split("\n").length;
        r.removed = 0;
      }
      break;
    }
    case "search": {
      const n = num(o.numMatches) ?? num(o.totalMatches) ?? num(o.numFiles);
      if (n !== undefined) r.matches = n;
      break;
    }
  }
  return Object.keys(r).length > 0 ? r : undefined;
}

// Todo keeps the session's todo list: TodoWrite replaces it, the Task tools
// change it item by item (todo-tracking page; TaskCreate's id arrives in its
// result, TaskCreateOutput.task.id). The list lives in this process only:
// after a resume it is not known (Flow is translation only, it reads no
// Claude Code task files), so Task tool changes are tracked but not emitted
// until a whole list arrives (TaskList's result, or TodoWrite). Emitting the
// partial list would replace the owner's list with its few known items.
export class TodoList {
  private items: TodoItem[] = [];
  private creating = new Map<string, TodoItem>();
  private complete: boolean;

  // complete: the list is known from its start (a fresh session).
  constructor(complete = true) {
    this.complete = complete;
  }

  snapshot(): TodoItem[] {
    return this.items.map((i) => ({ ...i }));
  }

  // fromTodoWrite replaces the list with TodoWrite's input; false when the
  // input is not a todo list.
  fromTodoWrite(input: Input): boolean {
    if (!Array.isArray(input.todos)) return false;
    const items: TodoItem[] = [];
    for (const t of input.todos) {
      const o = obj(t);
      const text = str(o?.content);
      const status = todoStatus(o?.status);
      if (o === undefined || text === undefined || status === undefined)
        continue;
      const item: TodoItem = { text, status };
      const active = str(o.activeForm);
      if (active !== undefined) item.activeText = active;
      items.push(item);
    }
    this.items = items;
    this.complete = true;
    return true;
  }

  // started notes a Task tool call; the list changes when its result arrives.
  started(toolId: string, name: string, input: Input): void {
    if (name !== "TaskCreate") return;
    const text = str(input.subject);
    if (text === undefined) return;
    const item: TodoItem = { text, status: "pending" };
    const active = str(input.activeForm);
    if (active !== undefined) item.activeText = active;
    this.creating.set(toolId, item);
  }

  // finished applies a successful Task tool call; true when the list changed.
  finished(
    toolId: string,
    name: string,
    input: Input,
    output: unknown,
    ok: boolean,
  ): boolean {
    const pending = this.creating.get(toolId);
    this.creating.delete(toolId);
    if (!ok) return false;
    const o = obj(output);
    switch (name) {
      case "TaskCreate": {
        const task = obj(o?.task);
        const id = str(task?.id);
        if (pending === undefined || id === undefined) return false;
        this.items.push({ id, ...pending });
        return this.complete;
      }
      case "TaskUpdate": {
        const id = str(input.taskId);
        if (id === undefined || o?.success === false) return false;
        const i = this.items.findIndex((t) => t.id === id);
        // An id this process never saw: nothing to change, nothing made up.
        if (i < 0) return false;
        if (input.status === "deleted") {
          this.items.splice(i, 1);
          return this.complete;
        }
        const item = this.items[i];
        const status = todoStatus(input.status);
        if (status !== undefined) item.status = status;
        const subject = str(input.subject);
        if (subject !== undefined) item.text = subject;
        const active = str(input.activeForm);
        if (active !== undefined) item.activeText = active;
        return this.complete;
      }
      case "TaskList": {
        // TaskListOutput carries the whole list: it rebuilds one that this
        // process did not see grow (a resumed session).
        if (!Array.isArray(o?.tasks)) return false;
        const known = new Map(this.items.map((t) => [t.id, t]));
        const items: TodoItem[] = [];
        for (const t of o.tasks) {
          const to = obj(t);
          const id = str(to?.id);
          const text = str(to?.subject);
          const status = todoStatus(to?.status);
          if (id === undefined || text === undefined || status === undefined)
            continue;
          const item: TodoItem = { id, text, status };
          const active = known.get(id)?.activeText;
          if (active !== undefined) item.activeText = active;
          items.push(item);
        }
        this.items = items;
        this.complete = true;
        return true;
      }
    }
    return false;
  }
}

function todoStatus(v: unknown): TodoStatus | undefined {
  return v === "pending" || v === "in_progress" || v === "completed"
    ? v
    : undefined;
}
