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

// isSearchShortcut: Cmd+K or Ctrl+K, without Shift or Alt. Ctrl+K typed in
// the terminal is left to the shell (it kills the line there); Cmd+K is not
// a terminal key and opens the search from anywhere.
export function isSearchShortcut(e: KeyboardEvent): boolean {
  if (e.key.toLowerCase() !== "k" || e.shiftKey || e.altKey) return false;
  if (e.metaKey) return true;
  if (!e.ctrlKey) return false;
  const target = e.target instanceof Element ? e.target : null;
  return !target?.closest(".tdr-screen");
}
