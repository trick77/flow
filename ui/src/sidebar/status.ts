import type { SessionStatus } from "../api/types.gen";

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

// statusClass picks the dot color; plan waits on the owner like a question.
export function statusClass(status: SessionStatus): string {
  if (status === "plan") return "st-question";
  if (status === "queued") return "st-working";
  return status in statusKeys ? `st-${status}` : "st-idle";
}
