import { sandboxBusy, sandboxStale } from "../api";
import type { Sandbox } from "../api/types.gen";

// The dot beside a project in the sidebar, coloured as the project card
// colours its sandbox: running, in progress, failed, or not running.
export type SandboxDot = "warm" | "busy" | "failed" | "down";

// sandboxDot maps a sandbox to its dot by the rule the card, the header chip
// and the session's notice share (api/sandbox.ts): in progress while an
// operation runs (busy); a setup or stop with nothing running did not finish
// and reads as failed, as the card says it.
export function sandboxDot(
  sandbox: Pick<Sandbox, "state" | "busy">,
): SandboxDot {
  if (sandboxBusy(sandbox)) return "busy";
  if (sandboxStale(sandbox)) return "failed";
  switch (sandbox.state) {
    case "warm":
      return "warm";
    case "deleting":
      return "busy";
    case "failed":
      return "failed";
    default:
      return "down";
  }
}
