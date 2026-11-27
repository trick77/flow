import { ApiError, answerLost, errorCode } from "../api";
import type {
  DeviceFlow,
  ErrorCode,
  GitHubConnection,
  GitHubRepo,
} from "../api/types.gen";

// RowState is what the Settings row shows for a connection.
export type RowState =
  | "unavailable"
  | "disconnected"
  | "connecting"
  | "connected"
  | "reconnect"
  | "error";

// rowState maps the API's state to the row. expired means the authorization
// can no longer be renewed by Flow: the owner reconnects. A state this UI
// does not know reads as error rather than as any of the others.
export function rowState(conn: GitHubConnection): RowState {
  if (conn.available === false) return "unavailable";
  switch (conn.state as string) {
    case "disconnected":
      return "disconnected";
    case "pending":
      return "connecting";
    case "connected":
      return "connected";
    case "expired":
      return "reconnect";
    default:
      return "error";
  }
}

// PollOutcome is one answer of pollGitHubDeviceFlow, read for the poll loop.
export type PollOutcome =
  | { kind: "pending" }
  | { kind: "slowDown"; intervalSeconds?: number }
  | { kind: "connected"; connection: GitHubConnection }
  | { kind: "denied" }
  | { kind: "expired" }
  | { kind: "unavailable" }
  | { kind: "retry" }
  | { kind: "failed"; code: ErrorCode };

// PollAnswer is the poll's 200 body read defensively: the state as any
// string, so GitHub's own words (slow_down, denied) are understood should
// the server pass them on.
type PollAnswer = Omit<GitHubConnection, "state"> & { state: string };

// pollOutcome is the one place that reads a poll's answer (or its failure).
// pending with intervalSeconds is a slow-down (the interval now in force);
// disconnected means the code was denied or the server holds none; expired,
// that the code expired. A 501 means the server cannot connect GitHub; a lost
// answer (incl. 502, GitHub not answering) is retried; any other refusal
// ends the flow.
export function pollOutcome(answer: unknown, err?: unknown): PollOutcome {
  if (err !== undefined) {
    if (err instanceof ApiError && err.code === "not_implemented") {
      return { kind: "unavailable" };
    }
    if (answerLost(err)) return { kind: "retry" };
    return { kind: "failed", code: errorCode(err) };
  }
  const a = (answer ?? {}) as PollAnswer;
  const interval =
    typeof a.intervalSeconds === "number" && a.intervalSeconds > 0
      ? a.intervalSeconds
      : undefined;
  switch (a.state) {
    case "pending":
      return interval === undefined
        ? { kind: "pending" }
        : { kind: "slowDown", intervalSeconds: interval };
    case "slow_down":
      return { kind: "slowDown", intervalSeconds: interval };
    case "connected":
      return {
        kind: "connected",
        connection: { ...a, state: "connected", available: true },
      };
    case "expired":
      return { kind: "expired" };
    case "denied":
    case "access_denied":
    case "disconnected":
      return { kind: "denied" };
    default:
      return { kind: "failed", code: "internal" };
  }
}

// SLOW_DOWN_STEP is what GitHub adds to the interval on a slow_down when no
// new interval comes with it (RFC 8628, 3.5: 5 seconds).
export const SLOW_DOWN_STEP_MS = 5000;

// nextInterval is the wait before the next poll: never shorter than the one
// in force, longer after a slow-down.
export function nextInterval(currentMs: number, o: PollOutcome): number {
  if (o.kind !== "slowDown") return currentMs;
  if (o.intervalSeconds === undefined) return currentMs + SLOW_DOWN_STEP_MS;
  return Math.max(currentMs, o.intervalSeconds * 1000);
}

// MIN_INTERVAL_MS guards an answer without a usable interval: GitHub's
// default (RFC 8628, 3.2: 5 seconds).
export const MIN_INTERVAL_MS = 5000;

// ActiveFlow is a device code being confirmed, in the form the poll loop
// uses: the expiry as epoch milliseconds.
export interface ActiveFlow {
  userCode: string;
  verificationUri: string;
  expiresAt: number;
  intervalMs: number;
}

export function activeFlow(f: DeviceFlow): ActiveFlow {
  const expiresAt = Date.parse(f.expiresAt);
  return {
    userCode: f.userCode,
    verificationUri: f.verificationUri,
    expiresAt: Number.isNaN(expiresAt) ? 0 : expiresAt,
    intervalMs:
      f.intervalSeconds > 0
        ? Math.max(f.intervalSeconds * 1000, 1000)
        : MIN_INTERVAL_MS,
  };
}

// countdown splits milliseconds left into whole minutes and seconds, rounded
// up (a code with 0.4 s left still shows 0:01, never 0:00 before it ends).
export function countdown(ms: number): { minutes: number; seconds: number } {
  const total = Math.max(0, Math.ceil(ms / 1000));
  return { minutes: Math.floor(total / 60), seconds: total % 60 };
}

// safeUrl keeps only an http(s) URL for a link the server hands over.
export function safeUrl(url: string): string | null {
  try {
    const u = new URL(url);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : null;
  } catch {
    return null;
  }
}

// repoKey reads one line of the repositories field as owner/name, lower
// case: "Example-Org/Repo", "https://github.com/example-org/repo.git" and
// "github.com/example-org/repo/" are the same repository.
export function repoKey(line: string): string {
  return line
    .trim()
    .replace(/^https?:\/\//i, "")
    .replace(/^(www\.)?github\.com\//i, "")
    .replace(/\/+$/, "")
    .replace(/\.git$/i, "")
    .toLowerCase();
}

function lines(text: string): string[] {
  return text.split("\n");
}

// hasRepo says whether the field names the repository already, in any form.
export function hasRepo(text: string, fullName: string): boolean {
  const key = repoKey(fullName);
  return lines(text).some((l) => l.trim() !== "" && repoKey(l) === key);
}

// toggleRepo adds the repository as a line of its own, or removes every line
// that names it (in whatever form it was typed). Other lines stay as typed.
export function toggleRepo(text: string, fullName: string): string {
  if (hasRepo(text, fullName)) {
    const key = repoKey(fullName);
    return lines(text)
      .filter((l) => l.trim() === "" || repoKey(l) !== key)
      .join("\n")
      .replace(/^\n+/, "")
      .replace(/\n{2,}$/, "\n");
  }
  const kept = text.replace(/\s+$/, "");
  return kept === "" ? fullName : `${kept}\n${fullName}`;
}

// MAX_SHOWN bounds the options the picker renders at once; the search
// narrows the rest.
export const MAX_SHOWN = 50;

// filterRepos is the picker's search over the list the server returned: a
// case-insensitive match on owner/name (and the description), repositories
// whose name starts with the query first, else in the server's order; at most
// limit, with the count left out.
export function filterRepos(
  items: GitHubRepo[],
  query: string,
  limit = MAX_SHOWN,
): { shown: GitHubRepo[]; more: number } {
  const q = query.trim().toLowerCase();
  let matches = items;
  if (q !== "") {
    const starts: GitHubRepo[] = [];
    const rest: GitHubRepo[] = [];
    for (const r of items) {
      const full = r.fullName.toLowerCase();
      const name = full.slice(full.indexOf("/") + 1);
      if (name.startsWith(q) || full.startsWith(q)) starts.push(r);
      else if (
        full.includes(q) ||
        (r.description ?? "").toLowerCase().includes(q)
      ) {
        rest.push(r);
      }
    }
    matches = [...starts, ...rest];
  }
  return {
    shown: matches.slice(0, limit),
    more: Math.max(0, matches.length - limit),
  };
}
