import type { SessionStatus, SessionSummary } from "../api/types.gen";

export type SectionId = "needs" | "working" | "done" | "idle";

export const SECTION_ORDER: readonly SectionId[] = [
  "needs",
  "working",
  "done",
  "idle",
];

// sectionOf follows docs/events.md: Needs you = approval, question, plan;
// Working = working, queued; Done = done, failed; Idle = the rest. (unseen
// is not used yet: done and failed always go to Done.)
export function sectionOf(status: SessionStatus): SectionId {
  switch (status) {
    case "approval":
    case "question":
    case "plan":
      return "needs";
    case "working":
    case "queued":
      return "working";
    case "done":
    case "failed":
      return "done";
    default:
      return "idle";
  }
}

export type Sections = Record<SectionId, SessionSummary[]>;

// groupSessions sorts sessions into the sections, most recent activity first.
export function groupSessions(sessions: readonly SessionSummary[]): Sections {
  const out: Sections = { needs: [], working: [], done: [], idle: [] };
  for (const s of sessions) out[sectionOf(s.status)].push(s);
  for (const id of SECTION_ORDER) {
    out[id].sort(
      (a, b) =>
        Date.parse(b.lastActivityAt) - Date.parse(a.lastActivityAt) ||
        a.id.localeCompare(b.id),
    );
  }
  return out;
}

// orderSessions is the sidebar's top-to-bottom order.
export function orderSessions(
  sessions: readonly SessionSummary[],
): SessionSummary[] {
  const g = groupSessions(sessions);
  return SECTION_ORDER.flatMap((id) => g[id]);
}
