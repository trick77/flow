// The permission callback's three faces (docs/events.md): an ordinary
// permission request, a question (AskUserQuestion) and a plan approval
// (ExitPlanMode) all arrive through canUseTool; each becomes its own event
// kind, and the owner's reply becomes the SDK's PermissionResult.

import { readFileSync } from "node:fs";

import type {
  PermissionResult,
  PermissionUpdate,
} from "@anthropic-ai/claude-agent-sdk";

import type {
  EventDraft,
  PermissionMode,
  PermissionReply,
  PermissionRequested,
  PlanReply,
  Question,
  QuestionReply,
} from "./contract/events.gen.js";
import { toSDKMode } from "./modes.js";
import { displayPath, permissionKind, toolFields } from "./tools.js";

export type RequestKind = "permission" | "question" | "plan";

// ToolRequest is one canUseTool call.
export interface ToolRequest {
  requestId: string;
  toolName: string;
  input: Record<string, unknown>;
  toolUseID: string;
  suggestions?: PermissionUpdate[];
  suppressAlwaysAllowRule?: boolean;
  blockedPath?: string;
}

export interface RequestContext {
  cwd: string;
  mode: PermissionMode;
  modeCap?: PermissionMode;
}

export function requestKind(toolName: string): RequestKind {
  if (toolName === "AskUserQuestion") return "question";
  if (toolName === "ExitPlanMode") return "plan";
  return "permission";
}

type Obj = Record<string, unknown>;

function obj(v: unknown): Obj | undefined {
  return v !== null && typeof v === "object" && !Array.isArray(v)
    ? (v as Obj)
    : undefined;
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v !== "" ? v : undefined;
}

// questions are AskUserQuestion's questions with positional ids: q<i> and
// o<j> are unique within the request, whose id is the provider's.
export function questions(input: Obj): Question[] {
  const qs = Array.isArray(input.questions) ? input.questions : [];
  return qs.map((q: unknown, i: number) => {
    const o = obj(q) ?? {};
    const opts = Array.isArray(o.options) ? o.options : [];
    const question: Question = {
      id: `q${i}`,
      text: String(o.question ?? ""),
      multiSelect: o.multiSelect === true,
      options: opts.map((opt: unknown, j: number) => {
        const p = obj(opt) ?? {};
        const option: Question["options"][number] = {
          id: `o${j}`,
          label: String(p.label ?? ""),
        };
        const description = str(p.description);
        if (description !== undefined) option.description = description;
        return option;
      }),
    };
    const header = str(o.header);
    if (header !== undefined) question.header = header;
    return question;
  });
}

// planText is ExitPlanMode's plan. The SDK types no longer list a plan field
// in ExitPlanModeInput (its output has plan and filePath), so the text comes
// from input.plan when present, else from a plan file the input names
// (provisional until a recorded session shows the input).
export function planText(input: Obj): string {
  const plan = str(input.plan);
  if (plan !== undefined) return plan;
  const file = str(input.planFilePath) ?? str(input.filePath);
  if (file !== undefined) {
    try {
      return readFileSync(file, "utf8");
    } catch {
      return "";
    }
  }
  return "";
}

export function requestDraft(
  req: ToolRequest,
  ctx: RequestContext,
): EventDraft {
  switch (requestKind(req.toolName)) {
    case "question":
      return {
        kind: "question.asked",
        data: {
          requestId: req.requestId,
          toolId: req.toolUseID,
          questions: questions(req.input),
        },
      };
    case "plan":
      return {
        kind: "plan.proposed",
        data: {
          requestId: req.requestId,
          toolId: req.toolUseID,
          plan: planText(req.input),
        },
      };
  }
  const fields = toolFields(req.toolName, req.input, ctx.cwd);
  const data: PermissionRequested = {
    requestId: req.requestId,
    toolId: req.toolUseID,
    tool: req.toolName,
    kind: permissionKind(req.toolName),
    cwd: ctx.cwd,
    input: req.input,
    mode: ctx.mode,
    canAlwaysAllow:
      (req.suggestions?.length ?? 0) > 0 &&
      req.suppressAlwaysAllowRule !== true,
  };
  if (fields.command !== undefined) data.command = fields.command;
  const path =
    req.blockedPath !== undefined
      ? displayPath(req.blockedPath, ctx.cwd)
      : fields.path;
  if (path !== undefined) data.path = path;
  if (ctx.modeCap !== undefined) data.modeCap = ctx.modeCap;
  return { kind: "permission.requested", data };
}

const DENIED = "The user denied this action.";
const PLAN_REJECTED = "The user did not approve the plan. Keep planning.";
const NO_ANSWER = "The user did not answer the question.";

export function permissionResult(
  req: ToolRequest,
  reply: PermissionReply,
): PermissionResult {
  switch (reply.decision) {
    case "allow_once":
      return { behavior: "allow", updatedInput: req.input };
    case "allow_always":
      return {
        behavior: "allow",
        updatedInput: req.input,
        ...(req.suggestions !== undefined &&
          req.suggestions.length > 0 && {
            updatedPermissions: req.suggestions,
          }),
      };
    default:
      return { behavior: "deny", message: reply.message || DENIED };
  }
}

// questionResult answers AskUserQuestion (user-input page): updatedInput is
// the input with answers keyed by question text, each the chosen labels
// joined with ", " (free text in place of or after them).
export function questionResult(
  req: ToolRequest,
  reply: QuestionReply,
): PermissionResult {
  const qs = questions(req.input);
  const answers: Record<string, string> = {};
  for (const q of qs) {
    const a = reply.answers?.find((x) => x.questionId === q.id);
    const parts: string[] = [];
    for (const id of a?.optionIds ?? []) {
      const label = q.options.find((o) => o.id === id)?.label;
      if (label !== undefined && label !== "") parts.push(label);
    }
    if (a?.text) parts.push(a.text);
    if (parts.length === 0 && reply.text) parts.push(reply.text);
    if (parts.length > 0) answers[q.text] = parts.join(", ");
  }
  if (Object.keys(answers).length === 0)
    return { behavior: "deny", message: NO_ANSWER };
  return { behavior: "allow", updatedInput: { ...req.input, answers } };
}

// planResult: approve = allow, with the mode the session continues in as a
// session-scoped setMode update; reject = deny with the owner's feedback
// (provisional: the SDK documents no approval path for ExitPlanMode).
export function planResult(
  req: ToolRequest,
  reply: PlanReply,
): PermissionResult {
  if (reply.decision !== "approved") {
    return { behavior: "deny", message: reply.feedback || PLAN_REJECTED };
  }
  const result: PermissionResult = {
    behavior: "allow",
    updatedInput: req.input,
  };
  if (reply.mode !== undefined) {
    result.updatedPermissions = [
      { type: "setMode", mode: toSDKMode(reply.mode), destination: "session" },
    ];
  }
  return result;
}
