import type { SessionSummary } from "../api/types.gen";
import {
  contains,
  highlight,
  MATCH_LIMIT,
  RECENT_LIMIT,
  searchSessions,
  terms,
} from "./search";

function s(
  id: string,
  title: string,
  at: string,
  over: Partial<SessionSummary> = {},
): SessionSummary {
  return {
    id,
    projectId: "p1",
    title,
    provider: "claude",
    model: "m",
    mode: "ask",
    status: "idle",
    unseen: false,
    repos: [],
    branch: `flow/${id}`,
    lastActivityAt: at,
    createdAt: at,
    ...over,
  };
}

const names: Record<string, string> = { p1: "riptide", p2: "harbor" };
const projectName = (id: string) => names[id] ?? "";
const ids = (q: string, list: SessionSummary[]) =>
  searchSessions(list, projectName, q).items.map((r) => r.session.id);

describe("terms", () => {
  it("splits on whitespace, lowercases and drops empties", () => {
    expect(terms("  Fix   the\tBUG ")).toEqual(["fix", "the", "bug"]);
    expect(terms("   ")).toEqual([]);
  });
});

describe("highlight", () => {
  it("marks every occurrence, case-insensitive", () => {
    expect(highlight("Test the test", ["test"])).toEqual([
      { text: "Test", hit: true },
      { text: " the ", hit: false },
      { text: "test", hit: true },
    ]);
  });

  it("merges overlapping and adjacent hits", () => {
    expect(highlight("abcd", ["abc", "bcd"])).toEqual([
      { text: "abcd", hit: true },
    ]);
    expect(highlight("aaa", ["aa"])).toEqual([{ text: "aaa", hit: true }]);
  });

  it("gives one plain part without terms and none for empty text", () => {
    expect(highlight("plain", [])).toEqual([{ text: "plain", hit: false }]);
    expect(highlight("", ["x"])).toEqual([]);
  });

  it("marks the original characters where lowercasing changes the length", () => {
    expect(highlight("İstanbul fix", ["fix"])).toEqual([
      { text: "İstanbul ", hit: false },
      { text: "fix", hit: true },
    ]);
  });

  it("takes terms literally, not as patterns", () => {
    expect(highlight("a.b axb", ["a.b"])).toEqual([
      { text: "a.b", hit: true },
      { text: " axb", hit: false },
    ]);
    expect(contains("cost (usd)", "(usd)")).toBe(true);
    expect(contains("axb", "a.b")).toBe(false);
  });
});

describe("searchSessions", () => {
  const list = [
    s("a", "Old title match", "2026-11-20T10:00:00Z"),
    s("b", "New title match", "2026-11-26T10:00:00Z"),
    s("c", "Something else", "2026-11-27T10:00:00Z", {
      branch: "fix/match-branch",
    }),
    s("d", "Unrelated", "2026-11-25T10:00:00Z", { projectId: "p2" }),
  ];

  it("lists the most recent sessions for an empty query", () => {
    const many = Array.from({ length: RECENT_LIMIT + 3 }, (_, i) =>
      s(
        `x${i}`,
        `t${i}`,
        `2026-11-${String(10 + i).padStart(2, "0")}T00:00:00Z`,
      ),
    );
    const out = searchSessions(many, projectName, "  ");
    expect(out.items).toHaveLength(RECENT_LIMIT);
    expect(out.total).toBe(RECENT_LIMIT);
    expect(out.items[0].session.id).toBe(`x${RECENT_LIMIT + 2}`);
    expect(out.items[0].title).toEqual([
      { text: out.items[0].session.title, hit: false },
    ]);
  });

  it("puts title matches first, then by recency", () => {
    const out = searchSessions(list, projectName, "MATCH").items;
    expect(out.map((r) => r.session.id)).toEqual(["b", "a", "c"]);
    expect(out[2].branch).toEqual([
      { text: "fix/", hit: false },
      { text: "match", hit: true },
      { text: "-branch", hit: false },
    ]);
  });

  it("ranks more terms in the title higher", () => {
    expect(
      ids("fix login", [
        s("one", "fix", "2026-11-27T00:00:00Z", { branch: "flow/login" }),
        s("two", "fix login", "2026-11-01T00:00:00Z"),
      ]),
    ).toEqual(["two", "one"]);
  });

  it("needs every term, in any field", () => {
    expect(ids("harbor unrelated", list)).toEqual(["d"]);
    expect(ids("harbor match", list)).toEqual([]);
    const d = searchSessions(list, projectName, "harbor").items[0];
    expect(d.project).toEqual([{ text: "harbor", hit: true }]);
  });

  it("caps the listed matches and counts them all", () => {
    const many = Array.from({ length: MATCH_LIMIT + 5 }, (_, i) =>
      s(`m${i}`, "same", "2026-11-01T00:00:00Z"),
    );
    const out = searchSessions(many, projectName, "same");
    expect(out.items).toHaveLength(MATCH_LIMIT);
    expect(out.total).toBe(MATCH_LIMIT + 5);
  });
});
