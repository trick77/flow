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

  it("takes Cmd+K and, off macOS, Ctrl+K, nothing else", () => {
    expect(isSearchShortcut(key({ key: "k", metaKey: true }), false)).toBe(
      true,
    );
    expect(isSearchShortcut(key({ key: "K", ctrlKey: true }), false)).toBe(
      true,
    );
    expect(isSearchShortcut(key({ key: "k" }), false)).toBe(false);
    expect(isSearchShortcut(key({ key: "j", metaKey: true }), false)).toBe(
      false,
    );
    expect(
      isSearchShortcut(key({ key: "k", metaKey: true, shiftKey: true }), false),
    ).toBe(false);
    expect(
      isSearchShortcut(key({ key: "k", ctrlKey: true, altKey: true }), false),
    ).toBe(false);
  });

  it("leaves Ctrl+K to the text fields on macOS", () => {
    expect(isSearchShortcut(key({ key: "k", ctrlKey: true }), true)).toBe(
      false,
    );
    expect(isSearchShortcut(key({ key: "k", metaKey: true }), true)).toBe(true);
  });

  it("ignores a keydown without a key", () => {
    const e = key({ metaKey: true });
    Object.defineProperty(e, "key", { value: undefined });
    expect(isSearchShortcut(e, false)).toBe(false);
  });

  it("leaves Ctrl+K in the terminal to the shell", () => {
    const screen = document.createElement("div");
    screen.className = "tdr-screen";
    const inner = document.createElement("textarea");
    screen.append(inner);
    expect(
      isSearchShortcut(key({ key: "k", ctrlKey: true }, inner), false),
    ).toBe(false);
    expect(
      isSearchShortcut(key({ key: "k", metaKey: true }, inner), false),
    ).toBe(true);
  });
});
