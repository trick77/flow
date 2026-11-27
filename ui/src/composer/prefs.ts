import type { Provider } from "../api/types.gen";

// What a new session starts with, as this browser last used it. Only a
// convenience: storage may be missing, full, blocked or hold anything, and
// every value is checked against the live lists before it is used. The mode
// is not among them: every session starts in plan mode (a mode stored by an
// earlier version is ignored).
export interface StartPrefs {
  projectId?: string;
  // The model's provider: two agents may offer the same model id. Prefs
  // stored before it was kept have none; the model then goes by id alone.
  provider?: Provider;
  model?: string;
}

export const PREFS_KEY = "flow.newSession";

export function loadPrefs(storage?: Storage): StartPrefs {
  try {
    const raw = (storage ?? window.localStorage).getItem(PREFS_KEY);
    const v: unknown = raw ? JSON.parse(raw) : null;
    if (!v || typeof v !== "object") return {};
    const o = v as Record<string, unknown>;
    const str = (x: unknown) => (typeof x === "string" ? x : undefined);
    return {
      projectId: str(o.projectId),
      provider: str(o.provider) as Provider | undefined,
      model: str(o.model),
    };
  } catch {
    return {};
  }
}

export function savePrefs(prefs: StartPrefs, storage?: Storage): void {
  try {
    (storage ?? window.localStorage).setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    // Private mode or a full store: the next start just begins from defaults.
  }
}
