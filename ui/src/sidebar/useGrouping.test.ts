import { act, renderHook, waitFor } from "@testing-library/react";

import { STORAGE_KEYS } from "../shell/storage";
import { useGrouping } from "./useGrouping";

type Answer = { status: number; body?: unknown };

function serve(get: Answer[], patch: (Answer | Promise<Answer>)[] = []) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      calls.push(`${method} ${init?.body ?? ""}`.trim());
      const a = await (method === "PATCH" ? patch.shift() : get.shift());
      if (!a) throw new Error("no answer");
      return new Response(a.body ? JSON.stringify(a.body) : null, {
        status: a.status,
      });
    }),
  );
  return calls;
}

const profile = (sidebarGrouping: string) => ({
  status: 200,
  body: {
    gitName: "",
    gitEmail: "",
    language: "en",
    formatLocale: "en-CH",
    timeZone: "Europe/Zurich",
    sidebarGrouping,
    configSync: { state: "never" },
  },
});

describe("useGrouping", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it("starts from this browser's copy, then takes the profile's", async () => {
    window.localStorage.setItem(STORAGE_KEYS.grouping, "project");
    serve([profile("status")]);
    const { result } = renderHook(() => useGrouping());
    expect(result.current[0]).toBe("project");
    await waitFor(() => expect(result.current[0]).toBe("status"));
    expect(window.localStorage.getItem(STORAGE_KEYS.grouping)).toBe("status");
  });

  it("keeps the browser's copy without a profile, and a stored junk value is status", async () => {
    window.localStorage.setItem(STORAGE_KEYS.grouping, "junk");
    serve([{ status: 501, body: { code: "not_implemented" } }, profile("x")]);
    const { result, rerender } = renderHook(({ rev }) => useGrouping(rev), {
      initialProps: { rev: 0 },
    });
    expect(result.current[0]).toBe("status");
    // A profile.updated reads it again; an unknown value changes nothing.
    rerender({ rev: 1 });
    await new Promise((r) => setTimeout(r, 0));
    expect(result.current[0]).toBe("status");
  });

  it("saves a choice to the profile and keeps it when that fails", async () => {
    const calls = serve(
      [profile("status")],
      [{ status: 500, body: { code: "internal" } }],
    );
    const { result } = renderHook(() => useGrouping());
    await waitFor(() => expect(calls).toHaveLength(1));
    act(() => result.current[1]("project"));
    expect(result.current[0]).toBe("project");
    await waitFor(() =>
      expect(calls).toContain('PATCH {"sidebarGrouping":"project"}'),
    );
    expect(window.localStorage.getItem(STORAGE_KEYS.grouping)).toBe("project");
    expect(result.current[0]).toBe("project");
  });

  it("never lets an older profile read override a newer choice", async () => {
    let answer: (a: Answer) => void = () => {};
    const pending = new Promise<Answer>((r) => {
      answer = r;
    });
    let saved: (a: Answer) => void = () => {};
    const saving = new Promise<Answer>((r) => {
      saved = r;
    });
    serve([pending as unknown as Answer, profile("status")], [saving]);
    const { result, rerender } = renderHook(({ rev }) => useGrouping(rev), {
      initialProps: { rev: 0 },
    });
    act(() => result.current[1]("project"));
    // The read asked before the choice answers with the old value.
    await act(async () => answer(profile("status")));
    expect(result.current[0]).toBe("project");
    // A read started while the choice is still being saved is ignored too.
    rerender({ rev: 1 });
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(result.current[0]).toBe("project");
    await act(async () => saved(profile("project")));
    expect(result.current[0]).toBe("project");
  });
});
