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

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// matcher finds a term case-insensitively in the original text, so its
// offsets are the text's own (lowercasing can change a string's length: "İ"
// becomes two code units).
function matcher(word: string): RegExp {
  return new RegExp(escape(word), "giu");
}

// contains: the term occurs in text, by the same rule highlight marks it.
export function contains(text: string, word: string): boolean {
  return matcher(word).test(text);
}

// highlight cuts text into parts, the occurrences of the terms marked as hits
// (case-insensitive; overlapping occurrences merge into one hit).
export function highlight(text: string, words: readonly string[]): Part[] {
  // hit[i]: code unit i is inside an occurrence of a term.
  const hit = new Array<boolean>(text.length).fill(false);
  for (const w of words) {
    const re = matcher(w);
    let prev = -1;
    for (let m = re.exec(text); m !== null; m = re.exec(text)) {
      // Never the same occurrence twice: whatever the engine does with
      // lastIndex, the loop ends.
      if (m.index <= prev) break;
      prev = m.index;
      hit.fill(true, m.index, m.index + m[0].length);
      // Overlapping occurrences: look again one code point past this one's
      // start. One code unit would land inside a surrogate pair (an emoji),
      // where the u flag finds the same match again.
      re.lastIndex = m.index + ((m[0].codePointAt(0) ?? 0) > 0xffff ? 2 : 1);
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

// The palette's list and how many sessions matched in all (items holds at
// most MATCH_LIMIT of them).
export interface SearchResults {
  items: SearchResult[];
  total: number;
}

// searchSessions is the palette's list. Without terms: the most recently
// active sessions. With terms: the sessions where every term occurs in the
// title, the project name or the branch (case-insensitive); those with more
// of the terms in the title first, then by recency.
export function searchSessions(
  sessions: readonly SessionSummary[],
  projectName: (projectId: string) => string,
  query: string,
): SearchResults {
  const words = terms(query);
  const result = (s: SessionSummary): SearchResult => ({
    session: s,
    title: highlight(s.title, words),
    project: highlight(projectName(s.projectId), words),
    branch: highlight(s.branch, words),
  });
  if (words.length === 0) {
    const items = [...sessions]
      .sort(recency)
      .slice(0, RECENT_LIMIT)
      .map(result);
    return { items, total: items.length };
  }
  const scored: { s: SessionSummary; inTitle: number }[] = [];
  for (const s of sessions) {
    const project = projectName(s.projectId);
    const inTitle = words.filter((w) => contains(s.title, w)).length;
    if (
      words.every(
        (w) =>
          contains(s.title, w) || contains(project, w) || contains(s.branch, w),
      )
    ) {
      scored.push({ s, inTitle });
    }
  }
  // More of the terms in the title ranks higher; ties go by recency.
  scored.sort((a, b) => b.inTitle - a.inTitle || recency(a.s, b.s));
  return {
    items: scored.slice(0, MATCH_LIMIT).map(({ s }) => result(s)),
    total: scored.length,
  };
}
