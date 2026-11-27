import { useEffect, useSyncExternalStore } from "react";

import { errorCode, listAgents } from "../api";
import type {
  AgentCatalog,
  ClaudeAuth,
  Effort,
  ErrorCode,
  ModelInfo,
  PermissionMode,
  Project,
  Provider,
  ProviderInfo,
} from "../api/types.gen";
import type { StartPrefs } from "./prefs";

export type CatalogState =
  | { state: "loading" }
  | { state: "failed"; error: ErrorCode }
  | { state: "ready"; catalog: AgentCatalog };

// One catalog for the whole app: every composer and New session read the
// same state, so a load that succeeds anywhere names the models everywhere.
let cached: Promise<AgentCatalog> | null = null;
let current: CatalogState = { state: "loading" };
// The last catalog that loaded: a later failed refresh keeps it naming the
// models in the composers.
let known: AgentCatalog | undefined;
const listeners = new Set<() => void>();

function publish(next: CatalogState) {
  current = next;
  if (next.state === "ready") known = next.catalog;
  listeners.forEach((fn) => fn());
}

// loadCatalog asks GET /api/agents once and keeps the answer; fresh asks
// again (a credential may have been added since). A failure is not kept. A
// fresh ask over a loaded catalog keeps showing it until the answer is in.
export function loadCatalog(fresh = false): Promise<AgentCatalog> {
  if (!cached || fresh) {
    const p = listAgents();
    cached = p;
    if (current.state !== "ready") publish({ state: "loading" });
    p.then(
      (catalog) => {
        if (cached === p) publish({ state: "ready", catalog });
      },
      (err: unknown) => {
        if (cached !== p) return;
        cached = null;
        publish({ state: "failed", error: errorCode(err) });
      },
    );
  }
  return cached;
}

// resetCatalog drops the kept answer (tests).
export function resetCatalog(): void {
  cached = null;
  known = undefined;
  current = { state: "loading" };
  listeners.forEach((fn) => fn());
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

// useCatalog is the shared catalog state, loaded on first use (asked again
// on mount when fresh); retry asks again.
export function useCatalog(fresh = false): [CatalogState, () => void] {
  const state = useSyncExternalStore(subscribe, () => current);
  useEffect(() => {
    // A rejection is in the state already.
    loadCatalog(fresh).catch(() => {});
  }, [fresh]);
  const retry = () => {
    loadCatalog(true).catch(() => {});
  };
  return [state, retry];
}

// useKnownCatalog is the last catalog that loaded, for naming models; it
// asks for one when there is none yet.
export function useKnownCatalog(): AgentCatalog | undefined {
  useSyncExternalStore(subscribe, () => current);
  useEffect(() => {
    loadCatalog().catch(() => {});
  }, []);
  return known;
}

// refreshCatalog asks again in the background (New session on show: a
// credential may have been added in Settings meanwhile).
export function refreshCatalog(): void {
  loadCatalog(true).catch(() => {});
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

// choiceKey identifies a model across providers: two agents may offer the
// same model id. Provider names hold no colon, so the first one splits.
export function choiceKey(provider: Provider, model: string): string {
  return `${provider}:${model}`;
}

export function parseChoiceKey(
  key: string,
): { provider: Provider; model: string } | undefined {
  const i = key.indexOf(":");
  if (i <= 0) return undefined;
  return { provider: key.slice(0, i) as Provider, model: key.slice(i + 1) };
}

// findModel is the model offered by an available provider: that provider's
// when one is given, else the first that offers it.
export function findModel(
  catalog: AgentCatalog,
  model: string | undefined,
  provider?: Provider,
): Choice | undefined {
  for (const p of catalog.providers) {
    if (!p.available) continue;
    if (provider !== undefined && p.provider !== provider) continue;
    const m = p.models.find((x) => x.id === model);
    if (m) return { provider: p, model: m };
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
  const remembered = findModel(catalog, prefs.model, prefs.provider);
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

// defaultMode is the mode every new session starts in: plan, else the most
// careful the catalog offers (its list runs from least to most permissive).
// Never remembered: a session started in a looser mode must not make the
// next one start there.
export function defaultMode(catalog: AgentCatalog): PermissionMode | undefined {
  if (catalog.modes.includes("plan")) return "plan";
  return catalog.modes[0];
}

// efforts are the efforts a provider offers, default (the agent's own) first;
// none when it takes no effort.
export function efforts(provider: ProviderInfo | undefined): Effort[] {
  const levels = (provider?.efforts ?? []).filter((e) => e !== "default");
  return levels.length > 0 ? ["default", ...levels] : [];
}

// defaultEffort is the effort a start begins with: the remembered one while
// the provider still offers it, else the provider's default (else the
// agent's own). Undefined when the provider takes no effort.
export function defaultEffort(
  provider: ProviderInfo,
  prefs: StartPrefs,
): Effort | undefined {
  const offered = efforts(provider);
  if (offered.length === 0) return undefined;
  if (prefs.effort && offered.includes(prefs.effort)) return prefs.effort;
  const d = provider.defaultEffort;
  return d && offered.includes(d) ? d : "default";
}

// defaultAuth is the Claude credential a start uses: the stored setup-token
// when there is one (the server's own default), else the first usable.
export function defaultAuth(provider: ProviderInfo): ClaudeAuth | undefined {
  const auths = provider.claudeAuths ?? [];
  if (auths.includes("subscription_token")) return "subscription_token";
  return auths[0];
}

// defaultProject is the project of the session on screen (current), else the
// remembered one while it exists, else the first.
export function defaultProject(
  projects: readonly Project[],
  prefs: StartPrefs,
  current?: string,
): Project | undefined {
  return (
    projects.find((p) => p.id === current) ??
    projects.find((p) => p.id === prefs.projectId) ??
    projects[0]
  );
}

// aboveCap: mode is more permissive than cap in the catalog's order (least
// to most permissive). Without a cap, or a mode the list lacks, nothing is.
export function aboveCap(
  modes: readonly PermissionMode[],
  mode: PermissionMode,
  cap: PermissionMode | undefined,
): boolean {
  if (!cap) return false;
  const at = modes.indexOf(mode);
  const capAt = modes.indexOf(cap);
  return at >= 0 && capAt >= 0 && at > capAt;
}
