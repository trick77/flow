import type { SessionStatus, SessionSummary } from "../api/types.gen";

const statusKeys = {
  approval: "statusApproval",
  question: "statusQuestion",
  plan: "statusPlan",
  working: "statusWorking",
  queued: "statusQueued",
  done: "statusDone",
  failed: "statusFailed",
  idle: "statusIdle",
} as const satisfies Record<SessionStatus, string>;

export type StatusKey = (typeof statusKeys)[SessionStatus];

// statusKey is the sidebar namespace's word for a status; an unknown one
// (a newer server) reads as idle.
export function statusKey(status: SessionStatus): StatusKey {
  return (statusKeys as Record<string, StatusKey>)[status] ?? "statusIdle";
}

// showsTime: a row shows how long ago its last activity was instead of its
// status word only when nothing is left to say: idle, or done and seen (the
// mock). Seeing a session takes only the bold title away; a failed one keeps
// its word and colour, in whichever section it sits.
export function showsTime(
  s: Pick<SessionSummary, "status" | "unseen">,
): boolean {
  return (
    statusKey(s.status) === "statusIdle" || (s.status === "done" && !s.unseen)
  );
}

// statusClass picks the dot color; plan waits on the owner like a question.
export function statusClass(status: SessionStatus): string {
  if (status === "plan") return "st-question";
  if (status === "queued") return "st-working";
  return status in statusKeys ? `st-${status}` : "st-idle";
}
