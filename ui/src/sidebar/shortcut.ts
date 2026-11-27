// isMac: the platform names its command key ⌘.
export function isMac(
  nav: Pick<Navigator, "platform" | "userAgent"> = navigator,
) {
  return /Mac|iPhone|iPad/.test(nav.platform || nav.userAgent);
}

// shortcutKey is the sidebar namespace's spelling of the search shortcut on
// this platform.
export function shortcutKey(mac = isMac()): "shortcutMac" | "shortcutOther" {
  return mac ? "shortcutMac" : "shortcutOther";
}

// isSearchShortcut: Cmd+K, or off macOS Ctrl+K, without Shift or Alt. On
// macOS Ctrl+K kills to the end of the line in every text field, so only Cmd+K
// opens the search there. Ctrl+K typed in the terminal is left to the shell (it
// kills the line there too); Cmd+K is not a terminal key and opens the search
// from anywhere. A keydown without a key (autofill fires those) is none.
export function isSearchShortcut(e: KeyboardEvent, mac = isMac()): boolean {
  if (typeof e.key !== "string") return false;
  if (e.key.toLowerCase() !== "k" || e.shiftKey || e.altKey) return false;
  if (e.metaKey) return true;
  if (!e.ctrlKey || mac) return false;
  const target = e.target instanceof Element ? e.target : null;
  return !target?.closest(".tdr-screen");
}
