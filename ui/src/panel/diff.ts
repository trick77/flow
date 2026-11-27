import type { DiffFile, DiffHunk } from "../api/types.gen";

// A line of a hunk as the panel shows it: its kind, the line numbers it has
// on the old and the new side, and its text without the prefix.
export interface DiffRow {
  kind: "ctx" | "add" | "del" | "note";
  old?: number;
  new?: number;
  text: string;
}

// numberHunk numbers a hunk's lines from its starts: a removed line counts on
// the old side, an added one on the new side, context on both. A "\ No
// newline at end of file" marker is a note and counts nowhere. A line
// without a known prefix (an empty context line some tools write) is context.
export function numberHunk(h: DiffHunk): DiffRow[] {
  let o = h.oldStart;
  let n = h.newStart;
  return h.lines.map((line): DiffRow => {
    const p = line.charAt(0);
    const text = line.slice(1);
    if (p === "+") return { kind: "add", new: n++, text };
    if (p === "-") return { kind: "del", old: o++, text };
    if (p === "\\") return { kind: "note", text: line };
    return { kind: "ctx", old: o++, new: n++, text: p === " " ? text : line };
  });
}

// diffTotals sums the added and removed lines of the files.
export function diffTotals(files: readonly DiffFile[]): {
  added: number;
  removed: number;
} {
  return files.reduce(
    (t, f) => ({ added: t.added + f.added, removed: t.removed + f.removed }),
    { added: 0, removed: 0 },
  );
}

// Panel widths: the mock's 460 px by default; never narrower than 320 px nor
// so wide that the timeline keeps less than 420 px.
export const PANEL_DEFAULT = 460;
export const PANEL_MIN = 320;
const TIMELINE_MIN = 420;

// panelMax is the widest the panel may be beside a centre of viewport px.
export function panelMax(viewport: number): number {
  return Math.max(PANEL_MIN, viewport - TIMELINE_MIN);
}

// clampWidth keeps a width (also one read from storage) within bounds;
// anything that is no number gives the default.
export function clampWidth(width: unknown, viewport: number): number {
  const w =
    typeof width === "number"
      ? width
      : typeof width === "string" && width.trim() !== ""
        ? Number(width)
        : NaN;
  if (!Number.isFinite(w)) return Math.min(PANEL_DEFAULT, panelMax(viewport));
  return Math.round(Math.min(Math.max(w, PANEL_MIN), panelMax(viewport)));
}
