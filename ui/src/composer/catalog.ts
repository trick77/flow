import { useEffect, useState } from "react";

import { errorCode, listAgents } from "../api";
import type {
  AgentCatalog,
  ClaudeAuth,
  ErrorCode,
  ModelInfo,
  PermissionMode,
  Project,
  Provider,
  ProviderInfo,
} from "../api/types.gen";
import type { StartPrefs } from "./prefs";

let cached: Promise<AgentCatalog> | null = null;

// loadCatalog asks GET /api/agents once and keeps the answer; fresh asks
// again (a credential may have been added since). A failure is not kept.
export function loadCatalog(fresh = false): Promise<AgentCatalog> {
  if (!cached || fresh) {
    const p = listAgents();
    cached = p;
    p.catch(() => {
      if (cached === p) cached = null;
    });
  }
  return cached;
}

// resetCatalog drops the kept answer (tests).
export function resetCatalog(): void {
  cached = null;
}

export type CatalogState =
  | { state: "loading" }
  | { state: "failed"; error: ErrorCode }
  | { state: "ready"; catalog: AgentCatalog };

// useCatalog is the agent catalog for a component; retry asks again.
export function useCatalog(fresh = false): [CatalogState, () => void] {
  const [state, setState] = useState<CatalogState>({ state: "loading" });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let live = true;
    loadCatalog(fresh || attempt > 0).then(
      (catalog) => live && setState({ state: "ready", catalog }),
      (err: unknown) =>
        live && setState({ state: "failed", error: errorCode(err) }),
    );
    return () => {
      live = false;
    };
  }, [fresh, attempt]);
  const retry = () => {
    setState({ state: "loading" });
    setAttempt((a) => a + 1);
  };
  return [state, retry];
}

// modelLabel is a model's name from the catalog, else its id.
export function modelLabel(
  catalog: AgentCatalog | undefined,
  provider: Provider,
  model: string,
): string {
  const p = catalog?.providers.find((x) => x.provider === provider);
  return p?.models.find((m) => m.id === model)?.label ?? model;
}

export interface Choice {
  provider: ProviderInfo;
  model: ModelInfo;
}

// findModel is the available provider offering the model.
export function findModel(
  catalog: AgentCatalog,
  model: string | undefined,
): Choice | undefined {
  for (const provider of catalog.providers) {
    if (!provider.available) continue;
    const m = provider.models.find((x) => x.id === model);
    if (m) return { provider, model: m };
  }
  return undefined;
}

// defaultChoice is the model a start begins with: the remembered one while an
// available provider still offers it, else the first available provider's
// default (or its first model). Undefined when no provider is available.
export function defaultChoice(
  catalog: AgentCatalog,
  prefs: StartPrefs,
): Choice | undefined {
  const remembered = findModel(catalog, prefs.model);
  if (remembered) return remembered;
  const provider = catalog.providers.find(
    (p) => p.available && p.models.length > 0,
  );
  if (!provider) return undefined;
  const model =
    provider.models.find((m) => m.id === provider.defaultModel) ??
    provider.models[0];
  return { provider, model };
}

// defaultMode is the remembered mode while the catalog lists it, else ask,
// else the least permissive.
export function defaultMode(
  catalog: AgentCatalog,
  prefs: StartPrefs,
): PermissionMode | undefined {
  if (prefs.mode && catalog.modes.includes(prefs.mode)) return prefs.mode;
  if (catalog.modes.includes("ask")) return "ask";
  return catalog.modes[0];
}

// defaultAuth is the Claude credential a start uses: the stored setup-token
// when there is one (the server's own default), else the first usable.
export function defaultAuth(provider: ProviderInfo): ClaudeAuth | undefined {
  const auths = provider.claudeAuths ?? [];
  if (auths.includes("subscription_token")) return "subscription_token";
  return auths[0];
}

// defaultProject is the remembered project while it exists, else the one
// given (the session on screen), else the first.
export function defaultProject(
  projects: readonly Project[],
  prefs: StartPrefs,
  current?: string,
): Project | undefined {
  return (
    projects.find((p) => p.id === prefs.projectId) ??
    projects.find((p) => p.id === current) ??
    projects[0]
  );
}
