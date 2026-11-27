import { useCallback, useSyncExternalStore } from "react";

// NARROW is the one breakpoint at which the sidebar becomes a drawer and the
// review panel takes the full width (index.css uses the same width).
export const NARROW = "(max-width: 900px)";

// useMediaQuery follows a media query; without matchMedia (tests, very old
// browsers) it never matches.
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window.matchMedia !== "function") return () => {};
      const mql = window.matchMedia(query);
      // Older engines (and some stubs) only have addListener.
      if (typeof mql.addEventListener !== "function") {
        mql.addListener?.(onChange);
        return () => mql.removeListener?.(onChange);
      }
      mql.addEventListener("change", onChange);
      return () => mql.removeEventListener("change", onChange);
    },
    [query],
  );
  const get = () =>
    typeof window.matchMedia === "function" && window.matchMedia(query).matches;
  return useSyncExternalStore(subscribe, get, () => false);
}
