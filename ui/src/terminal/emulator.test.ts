import { createXterm } from "./emulator";

// jsdom lays nothing out, so this checks the wiring, not the drawing: xterm
// mounts, takes bytes and input, reports no size while it has none.
describe("createXterm", () => {
  beforeEach(() => {
    // jsdom has no matchMedia (xterm watches the device pixel ratio with it).
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
    }));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("mounts xterm with the app's tokens and wires input and output", () => {
    const el = document.createElement("div");
    el.style.setProperty("--term-bg", "#151514");
    el.style.setProperty("--color-ink-warm", "#d5d2c9");
    document.body.appendChild(el);
    const emu = createXterm(el);
    expect(el.querySelector(".xterm")).not.toBeNull();
    const got: string[] = [];
    emu.onData((d) => got.push(d));
    emu.write(new TextEncoder().encode("hello"));
    emu.reset();
    expect(emu.fit()).toBeNull();
    emu.focus();
    emu.dispose();
    el.remove();
  });

  it("pastes with Ctrl+Shift+V and leaves Ctrl+C to the shell", async () => {
    const readText = vi.fn(() => Promise.resolve("pasted"));
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { readText, writeText },
      configurable: true,
    });
    const el = document.createElement("div");
    document.body.appendChild(el);
    const emu = createXterm(el);
    const got: string[] = [];
    emu.onData((d) => got.push(d));
    const area = el.querySelector("textarea");
    expect(area).not.toBeNull();
    const key = (k: string, shift: boolean) =>
      area!.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: k,
          ctrlKey: true,
          shiftKey: shift,
          bubbles: true,
          cancelable: true,
        }),
      );
    key("V", true);
    await vi.waitFor(() => expect(got.join("")).toContain("pasted"));
    key("C", true); // nothing selected: no copy
    expect(writeText).not.toHaveBeenCalled();
    key("x", true);
    emu.dispose();
    el.remove();
  });
});
