// The terminal drawer over the terminal WebSocket. Strings: terminal
// namespace. Owner: terminal area (docs/ownership.md).
export {
  TerminalDrawer,
  TerminalToggle,
  type TerminalTab,
} from "./TerminalDrawer";
export type { Emulator, EmulatorFactory } from "./emulator";
export {
  TerminalSession,
  type TerminalStatus,
  type WebSocketCtor,
  type WebSocketLike,
} from "./socket";
