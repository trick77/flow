import type { Sandbox } from "../api/types.gen";

// The dot beside a project in the sidebar, coloured as the project card
// colours its sandbox: running, in progress, failed, or not running.
export type SandboxDot = "warm" | "busy" | "failed" | "down";

// sandboxDot maps a sandbox to its dot. The one place that decides what
// reads as in progress: setup, stopping and deleting today.
export function sandboxDot(sandbox: Pick<Sandbox, "state">): SandboxDot {
  switch (sandbox.state) {
    case "warm":
      return "warm";
    case "setup":
    case "stopping":
    case "deleting":
      return "busy";
    case "failed":
      return "failed";
    default:
      return "down";
  }
}
