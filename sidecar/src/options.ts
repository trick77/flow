// The session specification the host starts the sidecar with, and the SDK
// options it becomes (docs/architecture.md, "Claude SDK settings").

import type {
  CanUseTool,
  McpStdioServerConfig,
  Options,
} from "@anthropic-ai/claude-agent-sdk";

import {
  CLAUDE_AUTH_VALUES,
  PERMISSION_MODE_VALUES,
  type ClaudeAuth,
  type PermissionMode,
} from "./contract/events.gen.js";
import { allowsFullAccess, toSDKMode } from "./modes.js";

// START_KIND is the sidecar's own first command, written by the host before
// any contract command: {"v":1,"id":…,"kind":"sidecar.start","data":StartSpec}.
// It is not a contract kind; only the host and the sidecar speak it.
export const START_KIND = "sidecar.start";

export interface StartSpec {
  // sessionId is Flow's session UUID, also the Claude session id.
  sessionId: string;
  // resume: the session ran before (any earlier host start); the SDK then
  // resumes it instead of creating it.
  resume: boolean;
  // cwd is the session directory.
  cwd: string;
  mode: PermissionMode;
  modeCap?: PermissionMode;
  model?: string;
  // claudeAuth only goes into session.started; flowd sets the credential.
  claudeAuth?: ClaudeAuth;
  // claudeExecutable is the image's Claude Code CLI.
  claudeExecutable: string;
  // settingsPath is the Flow settings layer, /etc/flow/claude-settings.json.
  settingsPath: string;
  // mcpServers: the flow server (flowd mcp), as the host configures it.
  mcpServers?: Record<string, McpStdioServerConfig>;
  // env is added over the sidecar's own environment for Claude Code.
  env?: Record<string, string>;
  additionalDirectories?: string[];
  // version names the Flow release in CLAUDE_AGENT_SDK_CLIENT_APP.
  version?: string;
  // priorCostUsd: on a resume, the cost of the session's earlier turns (the
  // SDK's total continues from it). Absent: the first turn reports no cost.
  priorCostUsd?: number;
}

function isStringRecord(v: unknown): v is Record<string, string> {
  return (
    v !== null &&
    typeof v === "object" &&
    !Array.isArray(v) &&
    Object.values(v).every((x) => typeof x === "string")
  );
}

// parseStartSpec checks a start command's data; the error names the field.
export function parseStartSpec(v: unknown): StartSpec | string {
  if (v === null || typeof v !== "object" || Array.isArray(v)) return "data";
  const s = v as Record<string, unknown>;
  for (const f of ["sessionId", "cwd", "claudeExecutable", "settingsPath"]) {
    if (typeof s[f] !== "string" || s[f] === "") return f;
  }
  if (typeof s.resume !== "boolean") return "resume";
  if (!PERMISSION_MODE_VALUES.includes(s.mode as PermissionMode)) return "mode";
  if (
    s.modeCap !== undefined &&
    !PERMISSION_MODE_VALUES.includes(s.modeCap as PermissionMode)
  ) {
    return "modeCap";
  }
  if (s.model !== undefined && typeof s.model !== "string") return "model";
  if (
    s.claudeAuth !== undefined &&
    !CLAUDE_AUTH_VALUES.includes(s.claudeAuth as ClaudeAuth)
  ) {
    return "claudeAuth";
  }
  if (s.env !== undefined && !isStringRecord(s.env)) return "env";
  if (s.mcpServers !== undefined) {
    if (s.mcpServers === null || typeof s.mcpServers !== "object")
      return "mcpServers";
    for (const c of Object.values(s.mcpServers)) {
      const m = c as Record<string, unknown>;
      if (typeof m?.command !== "string") return "mcpServers";
      if (
        m.args !== undefined &&
        !(Array.isArray(m.args) && m.args.every((a) => typeof a === "string"))
      ) {
        return "mcpServers";
      }
      if (m.env !== undefined && !isStringRecord(m.env)) return "mcpServers";
    }
  }
  if (
    s.additionalDirectories !== undefined &&
    !(
      Array.isArray(s.additionalDirectories) &&
      s.additionalDirectories.every((d) => typeof d === "string")
    )
  ) {
    return "additionalDirectories";
  }
  if (s.version !== undefined && typeof s.version !== "string")
    return "version";
  if (s.priorCostUsd !== undefined && typeof s.priorCostUsd !== "number")
    return "priorCostUsd";
  return s as unknown as StartSpec;
}

export interface QueryState {
  resume: boolean;
  // modeCap is the session's cap now (a settings.set may have moved it).
  modeCap?: PermissionMode;
  mode: PermissionMode;
  model?: string;
  canUseTool: CanUseTool;
  // env is the sidecar's own environment, credentials included (flowd set
  // exactly one); it is passed on and never logged.
  env: Record<string, string | undefined>;
  stderr: (data: string) => void;
}

export function buildOptions(spec: StartSpec, st: QueryState): Options {
  const o: Options = {
    cwd: spec.cwd,
    pathToClaudeCodeExecutable: spec.claudeExecutable,
    // Streaming input with partial messages: deltas live, the process kept
    // across turns.
    includePartialMessages: true,
    settingSources: ["user", "project", "local"],
    settings: spec.settingsPath,
    // Claude Code's own system prompt: the SDK's default is a minimal one.
    systemPrompt: { type: "preset", preset: "claude_code" },
    permissionMode: toSDKMode(st.mode),
    // bypassPermissions is refused later unless allowed at the start.
    allowDangerouslySkipPermissions: allowsFullAccess(st.modeCap),
    canUseTool: st.canUseTool,
    // env replaces the CLI's environment (typescript reference), so the
    // sidecar's own is spread first.
    env: {
      ...st.env,
      ...spec.env,
      // Flow's settings win over both environments above: the CLI is the
      // image's, never updated in place (docs/architecture.md, settings).
      DISABLE_AUTOUPDATER: "1",
      CLAUDE_CODE_ENTRYPOINT: "flow",
      CLAUDE_AGENT_SDK_CLIENT_APP: `flow/${spec.version ?? "dev"}`,
    },
    stderr: st.stderr,
  };
  if (st.model !== undefined) o.model = st.model;
  if (spec.mcpServers !== undefined) o.mcpServers = spec.mcpServers;
  if (spec.additionalDirectories !== undefined) {
    o.additionalDirectories = spec.additionalDirectories;
  }
  // sessionId on the first start, resume afterwards: the SDK refuses both
  // together (without forkSession).
  if (st.resume) o.resume = spec.sessionId;
  else o.sessionId = spec.sessionId;
  return o;
}
