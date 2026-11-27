import { loadPrefs, PREFS_KEY, savePrefs } from "./prefs";

describe("start prefs", () => {
  beforeEach(() => window.localStorage.clear());

  it("round-trips through localStorage", () => {
    savePrefs({
      projectId: "p1",
      provider: "opencode",
      model: "m",
      mode: "auto",
    });
    expect(loadPrefs()).toEqual({
      projectId: "p1",
      provider: "opencode",
      model: "m",
      mode: "auto",
    });
  });

  it("reads nothing from an empty, broken or odd store", () => {
    expect(loadPrefs()).toEqual({});
    window.localStorage.setItem(PREFS_KEY, "{not json");
    expect(loadPrefs()).toEqual({});
    window.localStorage.setItem(PREFS_KEY, "42");
    expect(loadPrefs()).toEqual({});
    window.localStorage.setItem(
      PREFS_KEY,
      JSON.stringify({ projectId: 7, model: "m", mode: null }),
    );
    expect(loadPrefs()).toEqual({
      projectId: undefined,
      model: "m",
      mode: undefined,
    });
  });

  it("survives a storage that throws", () => {
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("full");
      },
    } as unknown as Storage;
    expect(loadPrefs(broken)).toEqual({});
    expect(() => savePrefs({ model: "m" }, broken)).not.toThrow();
  });
});
