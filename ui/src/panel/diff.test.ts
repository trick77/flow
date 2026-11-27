import {
  clampWidth,
  diffTotals,
  numberHunk,
  PANEL_DEFAULT,
  PANEL_MIN,
  panelMax,
} from "./diff";

describe("numberHunk", () => {
  it("numbers context on both sides, removals old, additions new", () => {
    const rows = numberHunk({
      header: "@@ -10,4 +10,5 @@",
      oldStart: 10,
      oldLines: 4,
      newStart: 10,
      newLines: 5,
      lines: [" a", "-b", "+B", "+C", "", " d", "\\ No newline at end of file"],
    });
    expect(rows).toEqual([
      { kind: "ctx", old: 10, new: 10, text: "a" },
      { kind: "del", old: 11, text: "b" },
      { kind: "add", new: 11, text: "B" },
      { kind: "add", new: 12, text: "C" },
      { kind: "ctx", old: 12, new: 13, text: "" },
      { kind: "ctx", old: 13, new: 14, text: "d" },
      { kind: "note", text: "\\ No newline at end of file" },
    ]);
  });
});

describe("diffTotals", () => {
  it("sums the files' counts", () => {
    const f = (added: number, removed: number) => ({
      path: "p",
      change: "modified" as const,
      added,
      removed,
      hunks: [],
    });
    expect(diffTotals([f(3, 1), f(2, 0)])).toEqual({ added: 5, removed: 1 });
    expect(diffTotals([])).toEqual({ added: 0, removed: 0 });
  });
});

describe("panel width", () => {
  it("keeps the timeline 420 px and the panel at least 320 px", () => {
    expect(panelMax(1078)).toBe(658);
    expect(panelMax(500)).toBe(PANEL_MIN);
  });

  it("clamps stored and dragged widths, the default for anything else", () => {
    expect(clampWidth(null, 1078)).toBe(PANEL_DEFAULT);
    expect(clampWidth("junk", 1078)).toBe(PANEL_DEFAULT);
    expect(clampWidth("", 1078)).toBe(PANEL_DEFAULT);
    expect(clampWidth(undefined, 600)).toBe(PANEL_MIN);
    expect(clampWidth("540", 1078)).toBe(540);
    expect(clampWidth(100, 1078)).toBe(PANEL_MIN);
    expect(clampWidth(5000, 1078)).toBe(658);
    expect(clampWidth(500.6, 1078)).toBe(501);
  });
});
