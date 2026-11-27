import type { PermissionMode } from "../api/types.gen";

// What a new session starts with, as this browser last used it. Only a
// convenience: storage may be missing, full, blocked or hold anything, and
// every value is checked against the live lists before it is used.
export interface StartPrefs {
  projectId?: string;
  model?: string;
  mode?: PermissionMode;
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
      model: str(o.model),
      mode: str(o.mode) as PermissionMode | undefined,
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
