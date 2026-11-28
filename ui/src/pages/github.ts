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
  | { kind: "ended" }
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
// disconnected means the code was denied; with noCode the server holds no
// code (a restart, another tab ended it) and answers the connection as it
// stands: not connected, the flow simply ended. expired, that the code
// expired. A 501 means the server cannot connect GitHub. The server answers
// upstream or internal only once the code is over (GitHub away is pending),
// so those end the flow with their code, but only when the body carried the
// code: a proxy's 502/504 page is an answer lost on the way, which is
// retried; any other refusal ends the flow.
export function pollOutcome(answer: unknown, err?: unknown): PollOutcome {
  if (err !== undefined) {
    if (err instanceof ApiError && err.code === "not_implemented") {
      return { kind: "unavailable" };
    }
    if (
      err instanceof ApiError &&
      err.coded &&
      err.status >= 500 &&
      (err.code === "upstream" || err.code === "internal")
    ) {
      return { kind: "failed", code: err.code };
    }
    if (answerLost(err)) return { kind: "retry" };
    return { kind: "failed", code: errorCode(err) };
  }
  const a = (answer ?? {}) as PollAnswer;
  const interval =
    typeof a.intervalSeconds === "number" && a.intervalSeconds > 0
      ? a.intervalSeconds
      : undefined;
  if (a.noCode === true && a.state !== "connected") return { kind: "ended" };
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
// uses: the expiry as epoch milliseconds by this browser's clock.
export interface ActiveFlow {
  userCode: string;
  verificationUri: string;
  expiresAt: number;
  intervalMs: number;
}

// activeFlow converts the start answer received at now (this browser's
// clock). The deadline counts from the seconds the code has left, never from
// the server's absolute expiresAt: a browser clock ahead of the server's
// would expire the code early or at once. expiresAt is the fallback for a
// server that does not send the seconds.
export function activeFlow(f: DeviceFlow, now = Date.now()): ActiveFlow {
  const left = (f as Partial<DeviceFlow>).expiresInSeconds;
  const expiresAt =
    typeof left === "number" && left >= 0
      ? now + left * 1000
      : Date.parse(f.expiresAt);
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

// stripRepo drops what a pasted GitHub address carries around owner/name.
function stripRepo(text: string): string {
  return text
    .trim()
    .replace(/^https?:\/\//i, "")
    .replace(/^(www\.)?github\.com\//i, "")
    .replace(/\/+$/, "")
    .replace(/\.git$/i, "");
}

// repoKey is a repository's identity, lower case: "Example-Org/Repo",
// "https://github.com/example-org/repo.git" and "github.com/example-org/repo/"
// are the same repository.
export function repoKey(text: string): string {
  return stripRepo(text).toLowerCase();
}

// REPO_RE is the server's own check (projects.NormalizeRepo): an owner of up
// to 39 letters, digits or dashes, a name of letters, digits, dot, dash or
// underscore, not ending in a dot.
const REPO_RE = /^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9._-]{1,100}$/;

// parseRepo reads owner/name or a github.com address as owner/name, the case
// as typed (GitHub ignores it); null for anything the server would refuse.
export function parseRepo(text: string): string | null {
  const s = stripRepo(text);
  return REPO_RE.test(s) && !s.endsWith(".") ? s : null;
}

// repoName is the part after the slash: what a new project is called until
// the owner names it.
export function repoName(fullName: string): string {
  return fullName.slice(fullName.indexOf("/") + 1);
}

// MAX_SHOWN bounds the options the picker renders at once; the search
// narrows the rest.
export const MAX_SHOWN = 50;

// LIST_CAP is the most repositories the API lists (GET /api/github/repos):
// a list this long may have been cut.
export const LIST_CAP = 1000;

// isLocked: the repository is not public (the API's own flag).
export function isLocked(r: Pick<GitHubRepo, "private">): boolean {
  return r.private === true;
}
