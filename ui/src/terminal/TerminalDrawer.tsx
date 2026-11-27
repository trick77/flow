import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { createXterm, type EmulatorFactory } from "./emulator";
import { TerminalIcon, XIcon } from "./icons";
import {
  TerminalSession,
  type TerminalStatus,
  type WebSocketCtor,
} from "./socket";
import "./terminal.css";

// A terminal the drawer shows. One per session for now; the drawer keeps a
// list so more can follow (slots on the server: s-<session>-<n>).
export interface TerminalTab {
  key: string;
  projectId: string;
  sessionId?: string;
}

// fontsReady resolves once the web fonts are loaded: xterm measures its cell
// from the mono face, and measuring the fallback would leave the grid off.
function fontsReady(): Promise<unknown> {
  const fonts = (document as { fonts?: { ready: Promise<unknown> } }).fonts;
  return fonts?.ready ?? Promise.resolve();
}

// TerminalDrawer is the drawer under the conversation. It mounts its terminal
// the first time it opens and stays mounted while hidden, so the content stays.
export function TerminalDrawer({
  tab,
  open,
  onClose,
  WebSocket,
  createEmulator = createXterm,
}: {
  tab: TerminalTab;
  open: boolean;
  onClose: () => void;
  WebSocket?: WebSocketCtor;
  createEmulator?: EmulatorFactory;
}) {
  const { t } = useTranslation("terminal");
  const [opened, setOpened] = useState(open);
  if (open && !opened) setOpened(true);
  if (!opened) return null;
  return (
    <TerminalPane
      tab={tab}
      open={open}
      onClose={onClose}
      WebSocket={WebSocket}
      createEmulator={createEmulator}
      label={t("title")}
    />
  );
}

function useStatusText(s: TerminalStatus): string {
  const { t } = useTranslation("terminal");
  const { t: te } = useTranslation("errors");
  switch (s.kind) {
    case "connecting":
      return t("connecting");
    case "reconnecting":
      return t("disconnected");
    case "exited":
      return t("exited", { code: s.code });
    case "gone":
      return t("gone");
    case "stopped":
      return t("stopped");
    case "failed":
      return te(s.code);
    default:
      return "";
  }
}

function TerminalPane({
  tab,
  open,
  onClose,
  WebSocket,
  createEmulator,
  label,
}: {
  tab: TerminalTab;
  open: boolean;
  onClose: () => void;
  WebSocket?: WebSocketCtor;
  createEmulator: EmulatorFactory;
  label: string;
}) {
  const { t } = useTranslation("terminal");
  const screenRef = useRef<HTMLDivElement>(null);
  const sessionRef = useRef<TerminalSession | null>(null);
  const fitRef = useRef<() => void>(() => {});
  const focusRef = useRef<() => void>(() => {});
  const [status, setStatus] = useState<TerminalStatus>({ kind: "idle" });
  const [cwd, setCwd] = useState("");
  // The factories as the pane was mounted with: a parent's inline function must
  // not tear the terminal down on every render.
  const [made] = useState(() => ({ createEmulator, WebSocket }));

  // One emulator and one terminal session per tab, for the pane's lifetime.
  useEffect(() => {
    const el = screenRef.current;
    if (!el) return;
    let disposed = false;
    const emu = made.createEmulator(el);
    let last = "";
    const ts = new TerminalSession({
      projectId: tab.projectId,
      sessionId: tab.sessionId,
      size: () => emu.fit(),
      write: (b) => emu.write(b),
      reset: () => emu.reset(),
      onStatus: setStatus,
      onTerminal: (info) => setCwd(info.cwd),
      WebSocket: made.WebSocket,
    });
    sessionRef.current = ts;
    focusRef.current = () => emu.focus();
    emu.onData((d) => ts.send(d));
    fitRef.current = () => {
      const size = emu.fit();
      if (!size) return; // hidden: never measured, never sent
      const key = `${size.cols}x${size.rows}`;
      if (key !== last) {
        last = key;
        ts.resize(size.cols, size.rows);
      }
    };
    const ro =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(() => fitRef.current());
    ro?.observe(el);
    void fontsReady().then(() => {
      if (disposed) return;
      void ts.open();
      emu.focus();
    });
    return () => {
      disposed = true;
      ro?.disconnect();
      ts.dispose();
      emu.dispose();
      sessionRef.current = null;
      fitRef.current = () => {};
      focusRef.current = () => {};
    };
  }, [tab.key, tab.projectId, tab.sessionId, made]);

  // Showing the drawer again: fit to the size it has now, and take the focus.
  useEffect(() => {
    if (!open) return;
    fitRef.current();
    focusRef.current();
  }, [open]);

  // A size reached while connecting is sent once the socket is open.
  useEffect(() => {
    if (status.kind === "live") fitRef.current();
  }, [status]);

  const ended =
    status.kind === "exited" ||
    status.kind === "gone" ||
    status.kind === "stopped" ||
    status.kind === "failed";
  const note = useStatusText(status);
  return (
    <div
      className="tdrawer"
      hidden={!open}
      role="region"
      aria-label={label}
      data-status={status.kind}
    >
      <div className="tdr-h">
        <TerminalIcon />
        <span className="ttl">{t("title")}</span>
        <span className="mono tdr-path" title={cwd}>
          {cwd}
        </span>
        <span className="tdr-spacer" />
        {note !== "" && (
          <span
            className={`tdr-note ${ended ? "ended" : ""}`}
            role="status"
            aria-live="polite"
          >
            {note}
          </span>
        )}
        {ended && (
          <button
            type="button"
            className="tdr-btn"
            onClick={() => void sessionRef.current?.open()}
          >
            {t("restart")}
          </button>
        )}
        <button
          type="button"
          className="tdr-icon"
          aria-label={t("close")}
          title={t("close")}
          onClick={onClose}
        >
          <XIcon />
        </button>
      </div>
      <div className="tdr-screen" ref={screenRef} aria-label={t("output")} />
    </div>
  );
}

// TerminalToggle is the header button that shows and hides the drawer.
export function TerminalToggle({
  pressed,
  onToggle,
}: {
  pressed: boolean;
  onToggle: () => void;
}) {
  const { t } = useTranslation("terminal");
  return (
    <button
      type="button"
      className="tdr-toggle"
      aria-label={t("toggle")}
      aria-pressed={pressed}
      title={t("title")}
      onClick={onToggle}
    >
      <TerminalIcon />
    </button>
  );
}
