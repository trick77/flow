import { FitAddon } from "@xterm/addon-fit";
import { Terminal as XTerm, type ITheme } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

// Emulator is the drawer's view of the terminal emulator: xterm.js in the app,
// a fake in tests (jsdom cannot lay out xterm).
export interface Emulator {
  write(bytes: Uint8Array): void;
  reset(): void;
  // fit sizes the grid to its element; null when it has no size (hidden).
  fit(): { cols: number; rows: number } | null;
  focus(): void;
  onData(fn: (data: string) => void): void;
  dispose(): void;
}

export type EmulatorFactory = (el: HTMLElement) => Emulator;

// token reads a design token from the element's computed style: xterm needs
// concrete colours, not var() references.
function token(el: HTMLElement, name: string, fallback: string): string {
  const v = getComputedStyle(el).getPropertyValue(name).trim();
  return v === "" ? fallback : v;
}

function theme(el: HTMLElement): ITheme {
  const t = (name: string, fallback: string) => token(el, name, fallback);
  const ink = t("--color-ink-warm", "#d5d2c9");
  return {
    background: t("--term-bg", "#151514"),
    foreground: ink,
    cursor: ink,
    cursorAccent: t("--term-bg", "#151514"),
    selectionBackground: "rgba(143, 180, 217, 0.3)",
    black: t("--color-bubble", "#111110"),
    red: t("--color-danger", "#d9534f"),
    green: t("--color-success", "#5fa97a"),
    yellow: t("--color-sun", "#e0b46a"),
    blue: t("--color-cold", "#8fb4d9"),
    magenta: t("--color-aq-extreme", "#b98ad6"),
    cyan: t("--color-rain", "#7fb0de"),
    white: ink,
    brightBlack: t("--color-faint", "#6f6d66"),
    brightRed: t("--color-aq-very-poor", "#e06a66"),
    brightGreen: t("--color-aq-good", "#5fa97a"),
    brightYellow: t("--color-aq-moderate", "#e0b46a"),
    brightBlue: t("--color-cold", "#8fb4d9"),
    brightMagenta: t("--color-aq-extreme", "#b98ad6"),
    brightCyan: t("--color-rain", "#7fb0de"),
    brightWhite: t("--color-ink-bright", "#f3f0e8"),
  };
}

// createXterm mounts xterm.js in el, styled with the app's tokens and its mono
// face. Copy and paste: the browser's own shortcuts work on xterm's selection
// and textarea (Cmd+C/V; Ctrl+Insert, Shift+Insert); Ctrl+Shift+C and
// Ctrl+Shift+V are added for Linux and Windows, where Ctrl+C must stay ^C.
export function createXterm(el: HTMLElement): Emulator {
  const term = new XTerm({
    fontFamily: token(
      el,
      "--font-mono",
      '"Flow Mono", ui-monospace, SFMono-Regular, Menlo, monospace',
    ),
    fontSize: 12,
    lineHeight: 1.3,
    cursorBlink: true,
    scrollback: 5000,
    theme: theme(el),
    allowProposedApi: false,
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(el);
  term.attachCustomKeyEventHandler((ev) => {
    if (ev.type !== "keydown" || !ev.ctrlKey || !ev.shiftKey) return true;
    const key = ev.key.toLowerCase();
    if (key === "c" && term.hasSelection()) {
      void navigator.clipboard?.writeText(term.getSelection());
      return false;
    }
    if (key === "v") {
      void navigator.clipboard?.readText().then((text) => term.paste(text));
      return false;
    }
    return true;
  });
  return {
    write: (bytes) => term.write(bytes),
    reset: () => term.reset(),
    fit: () => {
      if (el.clientWidth === 0 || el.clientHeight === 0) return null;
      fit.fit();
      return { cols: term.cols, rows: term.rows };
    },
    focus: () => term.focus(),
    onData: (fn) => {
      term.onData(fn);
    },
    dispose: () => term.dispose(),
  };
}
