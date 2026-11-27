// Flow's permission modes and the SDK's (docs/architecture.md, "Mode
// mapping").

import type { PermissionMode as SDKPermissionMode } from "@anthropic-ai/claude-agent-sdk";

import type { PermissionMode } from "./contract/events.gen.js";

const TO_SDK: Record<PermissionMode, SDKPermissionMode> = {
  plan: "plan",
  ask: "default",
  accept_edits: "acceptEdits",
  auto: "auto",
  full_access: "bypassPermissions",
};

export function toSDKMode(m: PermissionMode): SDKPermissionMode {
  return TO_SDK[m];
}

// fromSDKMode is the Flow mode of an SDK mode; dontAsk has none.
export function fromSDKMode(m: string | undefined): PermissionMode | undefined {
  for (const [flow, sdk] of Object.entries(TO_SDK)) {
    if (sdk === m) return flow as PermissionMode;
  }
  return undefined;
}

const ORDER: readonly PermissionMode[] = [
  "plan",
  "ask",
  "accept_edits",
  "auto",
  "full_access",
];

// allowsFullAccess: the SDK refuses bypassPermissions unless the query was
// started with allowDangerouslySkipPermissions, so that flag is set exactly
// when the session's cap (if any) lets it reach full_access.
export function allowsFullAccess(cap: PermissionMode | undefined): boolean {
  return (
    cap === undefined || ORDER.indexOf(cap) >= ORDER.indexOf("full_access")
  );
}
