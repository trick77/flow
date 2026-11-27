import { isMac, isSearchShortcut, shortcutKey } from "./shortcut";

function key(init: KeyboardEventInit, target?: Element): KeyboardEvent {
  const e = new KeyboardEvent("keydown", init);
  if (target) Object.defineProperty(e, "target", { value: target });
  return e;
}

describe("search shortcut", () => {
  it("tells the platform and its label", () => {
    expect(isMac({ platform: "MacIntel", userAgent: "" })).toBe(true);
    expect(isMac({ platform: "", userAgent: "x (iPad; CPU OS)" })).toBe(true);
    expect(isMac({ platform: "Linux x86_64", userAgent: "" })).toBe(false);
    expect(shortcutKey(true)).toBe("shortcutMac");
    expect(shortcutKey(false)).toBe("shortcutOther");
  });

  it("takes Cmd+K and Ctrl+K, nothing else", () => {
    expect(isSearchShortcut(key({ key: "k", metaKey: true }))).toBe(true);
    expect(isSearchShortcut(key({ key: "K", ctrlKey: true }))).toBe(true);
    expect(isSearchShortcut(key({ key: "k" }))).toBe(false);
    expect(isSearchShortcut(key({ key: "j", metaKey: true }))).toBe(false);
    expect(
      isSearchShortcut(key({ key: "k", metaKey: true, shiftKey: true })),
    ).toBe(false);
    expect(
      isSearchShortcut(key({ key: "k", ctrlKey: true, altKey: true })),
    ).toBe(false);
  });

  it("leaves Ctrl+K in the terminal to the shell", () => {
    const screen = document.createElement("div");
    screen.className = "tdr-screen";
    const inner = document.createElement("textarea");
    screen.append(inner);
    expect(isSearchShortcut(key({ key: "k", ctrlKey: true }, inner))).toBe(
      false,
    );
    expect(isSearchShortcut(key({ key: "k", metaKey: true }, inner))).toBe(
      true,
    );
  });
});
