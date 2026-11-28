import type { Sandbox, SandboxState } from "./types.gen";

// The sandbox's state as the controls read it, one rule for the project
// card, the header chip, the session's notice and the review panel.
type SandboxLike = Pick<Sandbox, "state" | "busy">;

// sandboxBusy: an operation runs on the sandbox now (setup, start, stop,
// rebuild, delete); another one would wait or be refused with sandbox_busy.
export function sandboxBusy(sb?: Pick<Sandbox, "busy">): boolean {
  return sb?.busy === true;
}

// sandboxStale: setup or stopping with nothing running: the operation did
// not finish (Flow restarted mid-way, the host went down).
export function sandboxStale(sb?: SandboxLike): boolean {
  return (
    sb !== undefined &&
    !sandboxBusy(sb) &&
    (sb.state === "setup" || sb.state === "stopping")
  );
}

// sandboxStartable: a start helps now: nothing runs and the sandbox is not
// warm, nor being deleted. Without a sandbox known a start is offered.
export function sandboxStartable(sb?: SandboxLike): boolean {
  if (sb === undefined) return true;
  if (sandboxBusy(sb)) return false;
  const s: SandboxState = sb.state;
  return s !== "warm" && s !== "deleting";
}
