import type { ClaudeAuth, Effort, PermissionMode } from "../api/types.gen";

// The composer namespace's name and one-line hint of each permission mode.
export const modeKeys = {
  plan: { label: "modePlan", hint: "modePlanHint" },
  ask: { label: "modeAsk", hint: "modeAskHint" },
  accept_edits: { label: "modeAcceptEdits", hint: "modeAcceptEditsHint" },
  auto: { label: "modeAuto", hint: "modeAutoHint" },
  full_access: { label: "modeFullAccess", hint: "modeFullAccessHint" },
} as const satisfies Record<PermissionMode, { label: string; hint: string }>;

// The composer namespace's name of each effort; default is the agent's own.
export const effortKeys = {
  default: "effortDefault",
  low: "effortLow",
  medium: "effortMedium",
  high: "effortHigh",
  xhigh: "effortXhigh",
  max: "effortMax",
} as const satisfies Record<Effort, string>;

export const authKeys = {
  subscription_token: "authSubscriptionToken",
  subscription_login: "authSubscriptionLogin",
  api_key: "authApiKey",
} as const satisfies Record<ClaudeAuth, string>;
