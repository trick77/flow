import type { SessionSummary } from "../api/types.gen";

// A part of a field: matched by a search term or not.
export interface Part {
  text: string;
  hit: boolean;
}

export interface SearchResult {
  session: SessionSummary;
  title: Part[];
  project: Part[];
  branch: Part[];
}

// How many sessions the palette lists for an empty query, most recent first.
export const RECENT_LIMIT = 8;
// How many matches it lists at most.
export const MATCH_LIMIT = 50;

// terms splits a query into lowercase words; an empty query has none.
export function terms(query: string): string[] {
  return query.toLowerCase().split(/\s+/).filter(Boolean);
}

// highlight cuts text into parts, the occurrences of the terms marked as hits
// (case-insensitive; overlapping occurrences merge into one hit).
export function highlight(text: string, words: readonly string[]): Part[] {
  const lower = text.toLowerCase();
  // hit[i]: character i is inside an occurrence of a term.
  const hit = new Array<boolean>(text.length).fill(false);
  for (const w of words) {
    for (let i = lower.indexOf(w); i >= 0; i = lower.indexOf(w, i + 1)) {
      hit.fill(true, i, i + w.length);
    }
  }
  const parts: Part[] = [];
  for (let i = 0; i < text.length;) {
    let j = i;
    while (j < text.length && hit[j] === hit[i]) j++;
    parts.push({ text: text.slice(i, j), hit: hit[i] });
    i = j;
  }
  return parts;
}

const recency = (a: SessionSummary, b: SessionSummary) =>
  Date.parse(b.lastActivityAt) - Date.parse(a.lastActivityAt) ||
  a.id.localeCompare(b.id);

// searchSessions is the palette's list. Without terms: the most recently
// active sessions. With terms: the sessions where every term occurs in the
// title, the project name or the branch (case-insensitive); those with more
// of the terms in the title first, then by recency.
export function searchSessions(
  sessions: readonly SessionSummary[],
  projectName: (projectId: string) => string,
  query: string,
): SearchResult[] {
  const words = terms(query);
  const result = (s: SessionSummary): SearchResult => ({
    session: s,
    title: highlight(s.title, words),
    project: highlight(projectName(s.projectId), words),
    branch: highlight(s.branch, words),
  });
  if (words.length === 0) {
    return [...sessions].sort(recency).slice(0, RECENT_LIMIT).map(result);
  }
  const scored: { s: SessionSummary; inTitle: number }[] = [];
  for (const s of sessions) {
    const title = s.title.toLowerCase();
    const rest = `${projectName(s.projectId)}\n${s.branch}`.toLowerCase();
    if (words.every((w) => title.includes(w) || rest.includes(w))) {
      scored.push({
        s,
        inTitle: words.filter((w) => title.includes(w)).length,
      });
    }
  }
  // More of the terms in the title ranks higher; ties go by recency.
  scored.sort((a, b) => b.inTitle - a.inTitle || recency(a.s, b.s));
  return scored.slice(0, MATCH_LIMIT).map(({ s }) => result(s));
}
