import { useTranslation } from "react-i18next";

import type { PermissionMode, Provider } from "../contract/events.gen";

// The composer namespace's name of each permission mode, as the cards quote
// it.
export const modeNames = {
  plan: "modePlan",
  ask: "modeAsk",
  accept_edits: "modeAcceptEdits",
  auto: "modeAuto",
  full_access: "modeFullAccess",
} as const satisfies Record<PermissionMode, string>;

// useAgentName is the session's agent as the cards name it.
export function useAgentName(provider?: Provider): string {
  const { t } = useTranslation("timeline");
  if (provider === "claude") return t("agentClaude");
  if (provider === "opencode") return t("agentOpencode");
  return t("agentUnknown");
}
