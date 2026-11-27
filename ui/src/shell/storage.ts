// Per-browser conveniences (sidebar rail, grouping fallback, panel width) in
// localStorage. Only conveniences: storage may be missing, blocked, full or
// hold anything, so a read gives null and a write gives up quietly.
export function readStored(key: string, storage?: Storage): string | null {
  try {
    return (storage ?? window.localStorage).getItem(key);
  } catch {
    return null;
  }
}

export function writeStored(
  key: string,
  value: string,
  storage?: Storage,
): void {
  try {
    (storage ?? window.localStorage).setItem(key, value);
  } catch {
    // Private mode or a full store: the default comes back next time.
  }
}

// The keys, in one place so two areas never pick the same one.
export const STORAGE_KEYS = {
  rail: "flow.sidebar.rail",
  grouping: "flow.sidebar.grouping",
  panelWidth: "flow.panel.width",
} as const;
