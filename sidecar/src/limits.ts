// Size limits of the contract (docs/architecture.md, "Crash safety"; the
// capped fields in contract/events.schema.json). Every size is in UTF-8
// bytes, never in UTF-16 code units.

import type { EventDraft } from "./contract/events.gen.js";

// CAP is the bound for tool output, raw tool inputs, raw results and raw
// pass-through payloads: 16 KiB.
export const CAP = 16 << 10;

// MAX_LINE is the host's event.MaxLine. The host drops a longer line.
export const MAX_LINE = 1 << 20;

// LINE_BUDGET is what one draft line may take: the host wraps it in an
// envelope (v, seq, ts, session, turn) and re-encodes the payload with Go's
// HTML escaping, which goSize accounts for.
export const LINE_BUDGET = MAX_LINE - (16 << 10);

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function byteLength(s: string): number {
  return encoder.encode(s).length;
}

// goSize is the length of a JSON text once Go's encoding/json re-encodes it
// (the host stores drafts as json.RawMessage, which Marshal compacts with HTML
// escaping): <, > and & become six-byte \u escapes, U+2028 and U+2029 too.
export function goSize(json: string): number {
  let n = byteLength(json);
  for (const ch of json) {
    if (ch === "<" || ch === ">" || ch === "&") n += 5;
    else if (ch === " " || ch === " ") n += 3;
  }
  return n;
}

// headBytes is the longest prefix of s within max bytes, cut on a code point.
export function headBytes(s: string, max: number): string {
  const b = encoder.encode(s);
  if (b.length <= max) return s;
  let end = Math.max(0, max);
  // Back off continuation bytes (10xxxxxx) to the start of a code point.
  while (end > 0 && (b[end] & 0xc0) === 0x80) end--;
  return decoder.decode(b.subarray(0, end));
}

// tailBytes is the longest suffix of s within max bytes, cut on a code point.
export function tailBytes(s: string, max: number): string {
  const b = encoder.encode(s);
  if (b.length <= max) return s;
  let start = b.length - Math.max(0, max);
  while (start < b.length && (b[start] & 0xc0) === 0x80) start++;
  return decoder.decode(b.subarray(start));
}

const ELLIPSIS = "\n…\n";

// cutHeadTail keeps the head and the tail of a text longer than max bytes, the
// rule for tool output: "head and tail when longer".
export function cutHeadTail(
  s: string,
  max = CAP,
): { text: string; truncated: boolean } {
  if (byteLength(s) <= max) return { text: s, truncated: false };
  const room = max - byteLength(ELLIPSIS);
  const head = headBytes(s, Math.floor(room / 2));
  const tail = tailBytes(s, room - byteLength(head));
  return { text: head + ELLIPSIS + tail, truncated: true };
}

// capJSON is the rule for raw values (tool input, raw result, pass-through
// raw): as they are within CAP bytes of encoded JSON, else the first CAP bytes
// of that JSON as a string, truncated set.
export function capJSON(
  value: unknown,
  max = CAP,
): { value: unknown; truncated: boolean } {
  let json: string;
  try {
    json = JSON.stringify(value) ?? "null";
  } catch {
    return { value: "[not serializable]", truncated: true };
  }
  if (byteLength(json) <= max) return { value, truncated: false };
  return { value: headBytes(json, max), truncated: true };
}

// shortenStrings cuts every string inside value to head and tail of max
// bytes: the last resort for a draft that is still over the line budget.
function shortenStrings(value: unknown, max: number): unknown {
  if (typeof value === "string") return cutHeadTail(value, max).text;
  if (Array.isArray(value)) return value.map((v) => shortenStrings(v, max));
  if (value !== null && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value)) out[k] = shortenStrings(v, max);
    return out;
  }
  return value;
}

// headString is the longest prefix of s whose JSON string encoding stays
// within room bytes as Go writes it.
function headString(s: string, room: number): string {
  let max = room;
  for (;;) {
    const head = headBytes(s, max);
    const size = goSize(JSON.stringify(head));
    if (size <= room || head === "") return head;
    // Escaping makes the encoding longer than the text: shrink in proportion.
    max = Math.min(max - 1, Math.floor((max * room) / size));
  }
}

// Drafts the owner must answer: one the host never sees would leave the SDK
// waiting forever, so it is never swapped for a notice (the caller denies
// the request instead).
const REQUEST_KINDS: ReadonlySet<string> = new Set([
  "permission.requested",
  "question.asked",
  "plan.proposed",
]);

// The summary fields of a tool row, shortened before its input.
const SUMMARY_FIELDS = ["path", "command", "pattern", "url", "description"];

function fits(line: string): boolean {
  return goSize(line) <= LINE_BUDGET;
}

// fitDraft returns the draft's stdout line, shortened until the host can
// take it. A tool.started first has its summary fields cut, then its input
// becomes the first part of its JSON, as a string, with inputTruncated set
// (the contract's rule; set whenever the input changes). In any draft long
// strings (assistant text, a plan, a question) are then cut to head and
// tail. A draft still over the budget is replaced by a notice that keeps its
// command id, except a request: then fitDraft returns undefined.
export function fitDraft(d: EventDraft): string | undefined {
  let line = JSON.stringify(d);
  if (fits(line)) return line;
  let data = d.data as Record<string, unknown> | null;
  const tool =
    d.kind === "tool.started" && data !== null && typeof data === "object";
  if (tool && data !== null) {
    data = { ...data };
    for (const k of SUMMARY_FIELDS) {
      const v = data[k];
      if (typeof v === "string") data[k] = cutHeadTail(v).text;
    }
    line = JSON.stringify({ ...d, data });
    if (fits(line)) return line;
    if (data.input !== undefined) {
      const rest = { ...data, input: "", inputTruncated: true };
      const room = LINE_BUDGET - goSize(JSON.stringify({ ...d, data: rest }));
      data = {
        ...rest,
        input: headString(JSON.stringify(data.input) ?? "", Math.max(room, 0)),
      };
      line = JSON.stringify({ ...d, data });
      if (fits(line)) return line;
    }
  }
  for (const max of [LINE_BUDGET / 8, CAP * 4, CAP]) {
    const short = shortenStrings(data, max) as Record<string, unknown> | null;
    if (tool && short !== null && short.input !== undefined)
      short.inputTruncated = true;
    line = JSON.stringify({ ...d, data: short });
    if (fits(line)) return line;
  }
  if (REQUEST_KINDS.has(d.kind)) return undefined;
  const notice: EventDraft = {
    kind: "notice",
    data: {
      code: "draft_too_large",
      level: "warning",
      params: { kind: String(d.kind) },
    },
  };
  if (d.parentTool !== undefined) notice.parentTool = d.parentTool;
  if (d.cmd !== undefined) notice.cmd = d.cmd;
  return JSON.stringify(notice);
}
