import type {
  Project,
  SessionSummary,
  SidebarGrouping,
} from "../api/types.gen";

export type SectionId = "needs" | "working" | "done" | "idle";

export const SECTION_ORDER: readonly SectionId[] = [
  "needs",
  "working",
  "done",
  "idle",
];

// sectionOf follows docs/events.md: Needs you = approval, question, plan;
// Working = working, queued; Done = done, failed while unseen; Idle = the
// rest (a done or failed session the owner has seen among them).
export function sectionOf(
  s: Pick<SessionSummary, "status" | "unseen">,
): SectionId {
  switch (s.status) {
    case "approval":
    case "question":
    case "plan":
      return "needs";
    case "working":
    case "queued":
      return "working";
    case "done":
    case "failed":
      return s.unseen ? "done" : "idle";
    default:
      return "idle";
  }
}

export type Sections = Record<SectionId, SessionSummary[]>;

// byActivity: most recent activity first, the id breaking ties.
const byActivity = (a: SessionSummary, b: SessionSummary) =>
  Date.parse(b.lastActivityAt) - Date.parse(a.lastActivityAt) ||
  a.id.localeCompare(b.id);

// groupSessions sorts sessions into the sections, most recent activity first.
export function groupSessions(sessions: readonly SessionSummary[]): Sections {
  const out: Sections = { needs: [], working: [], done: [], idle: [] };
  for (const s of sessions) out[sectionOf(s)].push(s);
  for (const id of SECTION_ORDER) out[id].sort(byActivity);
  return out;
}

// orderSessions is the sidebar's top-to-bottom order by status (also the
// rail's order).
export function orderSessions(
  sessions: readonly SessionSummary[],
): SessionSummary[] {
  const g = groupSessions(sessions);
  return SECTION_ORDER.flatMap((id) => g[id]);
}

export interface ProjectSection {
  // The project's id; null for sessions of projects the list does not hold
  // (not loaded, or failed to load): they must not vanish.
  projectId: string | null;
  project?: Project;
  name?: string;
  sessions: SessionSummary[];
}

// groupByProject is the "By project" grouping: one section per project, one
// without sessions too (a new project must show before its first session),
// in the project list's order, sessions by latest activity; the sessions of
// unknown projects last, that section only when it holds any.
export function groupByProject(
  sessions: readonly SessionSummary[],
  projects: readonly Project[],
): ProjectSection[] {
  const known = new Map<string, ProjectSection>(
    projects.map((p) => [
      p.id,
      { projectId: p.id, project: p, name: p.name, sessions: [] },
    ]),
  );
  const other: ProjectSection = { projectId: null, sessions: [] };
  for (const s of sessions) (known.get(s.projectId) ?? other).sessions.push(s);
  return [...known.values(), other]
    .filter((sec) => sec.projectId !== null || sec.sessions.length > 0)
    .map((sec) => ({ ...sec, sessions: sec.sessions.sort(byActivity) }));
}

// lastUse is when each project was last worked in, in milliseconds: the
// later of its latest session activity and its sandbox's lastUsedAt (the
// server sets that on a sandbox start only, so it covers a project whose
// sessions are all archived). A project with neither is missing.
export function lastUse(
  sessions: readonly SessionSummary[],
  projects: readonly Project[],
): Map<string, number> {
  const out = new Map<string, number>();
  const note = (id: string, iso: string | undefined) => {
    const at = iso ? Date.parse(iso) : NaN;
    if (!Number.isNaN(at) && at > (out.get(id) ?? -Infinity)) out.set(id, at);
  };
  for (const s of sessions) note(s.projectId, s.lastActivityAt);
  for (const p of projects) note(p.id, p.sandbox.lastUsedAt);
  return out;
}

// isGrouping guards a stored or received value.
export function isGrouping(v: unknown): v is SidebarGrouping {
  return v === "status" || v === "project";
}
