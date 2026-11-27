import { useTranslation } from "react-i18next";

import type { Provider } from "../api/types.gen";

const names = {
  claude: { long: "agentClaude", short: "agentClaudeShort", cls: "prov-cc" },
  opencode: {
    long: "agentOpencode",
    short: "agentOpencodeShort",
    cls: "prov-oc",
  },
} as const satisfies Record<Provider, unknown>;

// agentName is the full name of a session's agent.
export function useAgentName(): (provider: Provider) => string {
  const { t } = useTranslation("common");
  return (provider) => t((names[provider] ?? names.claude).long);
}

// AgentPill is the small mark naming a session's agent: CC for Claude Code,
// OC for OpenCode. The short form is shown, the full name is read out.
export function AgentPill({ provider }: { provider: Provider }) {
  const { t } = useTranslation("common");
  const n = names[provider] ?? names.claude;
  return (
    <span className={`prov ${n.cls}`} title={t(n.long)}>
      <span aria-hidden="true">{t(n.short)}</span>
      <span className="sr">{t(n.long)}</span>
    </span>
  );
}
