import { renderHook, act } from "@testing-library/react";

import { readStored, STORAGE_KEYS, writeStored } from "./storage";
import { useMediaQuery } from "./useMediaQuery";

describe("storage", () => {
  afterEach(() => window.localStorage.clear());

  it("reads and writes, and gives up quietly when storage refuses", () => {
    writeStored(STORAGE_KEYS.rail, "1");
    expect(readStored(STORAGE_KEYS.rail)).toBe("1");
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("full");
      },
    } as unknown as Storage;
    expect(readStored("k", broken)).toBeNull();
    expect(() => writeStored("k", "v", broken)).not.toThrow();
  });
});

describe("useMediaQuery", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("never matches without matchMedia", () => {
    vi.stubGlobal("matchMedia", undefined);
    const { result } = renderHook(() => useMediaQuery("(max-width: 1px)"));
    expect(result.current).toBe(false);
  });

  it("follows the query's changes, also through the old listener API", () => {
    for (const legacy of [false, true]) {
      let matches = false;
      const listeners = new Set<() => void>();
      const add = (_t: string | (() => void), fn?: () => void) =>
        listeners.add((fn ?? _t) as () => void);
      const remove = (_t: string | (() => void), fn?: () => void) =>
        listeners.delete((fn ?? _t) as () => void);
      vi.stubGlobal("matchMedia", () =>
        legacy
          ? {
              get matches() {
                return matches;
              },
              addListener: add,
              removeListener: remove,
            }
          : {
              get matches() {
                return matches;
              },
              addEventListener: add,
              removeEventListener: remove,
            },
      );
      const { result, unmount } = renderHook(() => useMediaQuery("(q)"));
      expect(result.current).toBe(false);
      act(() => {
        matches = true;
        listeners.forEach((fn) => fn());
      });
      expect(result.current).toBe(true);
      unmount();
      expect(listeners.size).toBe(0);
    }
  });
});
