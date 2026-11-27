import { ApiError } from "../api";
import type { GitHubRepo } from "../api/types.gen";
import {
  activeFlow,
  countdown,
  filterRepos,
  hasRepo,
  MIN_INTERVAL_MS,
  nextInterval,
  pollOutcome,
  repoKey,
  rowState,
  safeUrl,
  SLOW_DOWN_STEP_MS,
  toggleRepo,
} from "./github";

describe("rowState", () => {
  it("maps each API state and an unknown one", () => {
    const on = { available: true };
    expect(rowState({ ...on, state: "disconnected" })).toBe("disconnected");
    expect(rowState({ ...on, state: "pending" })).toBe("connecting");
    expect(rowState({ ...on, state: "connected" })).toBe("connected");
    expect(rowState({ ...on, state: "expired" })).toBe("reconnect");
    expect(rowState({ ...on, state: "weird" as "pending" })).toBe("error");
  });

  it("says unavailable when the server has no GitHub App", () => {
    expect(rowState({ state: "disconnected", available: false })).toBe(
      "unavailable",
    );
    expect(rowState({ state: "connected", available: true })).toBe("connected");
  });
});

describe("pollOutcome", () => {
  it("reads the contract's states", () => {
    expect(pollOutcome({ state: "pending" })).toEqual({ kind: "pending" });
    expect(pollOutcome({ state: "expired" })).toEqual({ kind: "expired" });
    expect(pollOutcome({ state: "disconnected" })).toEqual({ kind: "denied" });
    expect(pollOutcome({ state: "connected", login: "octo-user" })).toEqual({
      kind: "connected",
      connection: { state: "connected", login: "octo-user", available: true },
    });
  });

  it("reads a slow-down, with or without a new interval", () => {
    expect(pollOutcome({ state: "pending", intervalSeconds: 10 })).toEqual({
      kind: "slowDown",
      intervalSeconds: 10,
    });
    expect(pollOutcome({ state: "slow_down" })).toEqual({
      kind: "slowDown",
      intervalSeconds: undefined,
    });
    expect(pollOutcome({ state: "pending", intervalSeconds: 0 })).toEqual({
      kind: "pending",
    });
  });

  it("reads a denial and anything unknown", () => {
    expect(pollOutcome({ state: "denied" })).toEqual({ kind: "denied" });
    expect(pollOutcome({ state: "access_denied" })).toEqual({
      kind: "denied",
    });
    expect(pollOutcome({ state: "what" })).toEqual({
      kind: "failed",
      code: "internal",
    });
    expect(pollOutcome(undefined)).toEqual({
      kind: "failed",
      code: "internal",
    });
  });

  it("reads failures: 501, a lost answer, a refusal", () => {
    expect(
      pollOutcome(undefined, new ApiError("not_implemented", 501)),
    ).toEqual({ kind: "unavailable" });
    expect(pollOutcome(undefined, new ApiError("internal", 0))).toEqual({
      kind: "retry",
    });
    // The server's upstream or internal: its code is over, no retry.
    expect(pollOutcome(undefined, new ApiError("upstream", 502))).toEqual({
      kind: "failed",
      code: "upstream",
    });
    expect(pollOutcome(undefined, new ApiError("internal", 500))).toEqual({
      kind: "failed",
      code: "internal",
    });
    expect(pollOutcome(undefined, new ApiError("conflict", 409))).toEqual({
      kind: "failed",
      code: "conflict",
    });
  });
});

describe("nextInterval", () => {
  it("never shortens, lengthens on a slow-down", () => {
    expect(nextInterval(5000, { kind: "pending" })).toBe(5000);
    expect(nextInterval(5000, { kind: "retry" })).toBe(5000);
    expect(nextInterval(5000, { kind: "slowDown" })).toBe(
      5000 + SLOW_DOWN_STEP_MS,
    );
    expect(nextInterval(5000, { kind: "slowDown", intervalSeconds: 12 })).toBe(
      12_000,
    );
    expect(nextInterval(15_000, { kind: "slowDown", intervalSeconds: 3 })).toBe(
      15_000,
    );
  });
});

describe("activeFlow", () => {
  it("converts the API's device code, its deadline by the local clock", () => {
    const now = Date.parse("2026-11-27T10:30:00Z"); // ahead of the server
    expect(
      activeFlow(
        {
          userCode: "ABCD-1234",
          verificationUri: "https://github.com/login/device",
          expiresAt: "2026-11-27T10:15:00Z",
          expiresInSeconds: 900,
          intervalSeconds: 5,
        },
        now,
      ),
    ).toEqual({
      userCode: "ABCD-1234",
      verificationUri: "https://github.com/login/device",
      expiresAt: now + 900_000,
      intervalMs: 5000,
    });
  });

  it("falls back to expiresAt without the seconds left", () => {
    const f = activeFlow({
      userCode: "X",
      verificationUri: "",
      expiresAt: "2026-11-27T10:15:00Z",
      intervalSeconds: 5,
    } as unknown as Parameters<typeof activeFlow>[0]);
    expect(f.expiresAt).toBe(Date.parse("2026-11-27T10:15:00Z"));
  });

  it("guards a missing interval and a bad expiry", () => {
    const f = activeFlow({
      userCode: "X",
      verificationUri: "",
      expiresAt: "soon",
      intervalSeconds: 0,
    } as unknown as Parameters<typeof activeFlow>[0]);
    expect(f.intervalMs).toBe(MIN_INTERVAL_MS);
    expect(f.expiresAt).toBe(0);
  });
});

describe("countdown", () => {
  it("rounds up and never goes negative", () => {
    expect(countdown(900_000)).toEqual({ minutes: 15, seconds: 0 });
    expect(countdown(61_500)).toEqual({ minutes: 1, seconds: 2 });
    expect(countdown(400)).toEqual({ minutes: 0, seconds: 1 });
    expect(countdown(-5)).toEqual({ minutes: 0, seconds: 0 });
  });
});

describe("safeUrl", () => {
  it("keeps http(s) only", () => {
    expect(safeUrl("https://github.com/login/device")).toBe(
      "https://github.com/login/device",
    );
    expect(safeUrl("javascript:alert(1)")).toBeNull();
    expect(safeUrl("not a url")).toBeNull();
  });
});

describe("repository lines", () => {
  it("reads every form as owner/name", () => {
    expect(repoKey(" Example-Org/Repo ")).toBe("example-org/repo");
    expect(repoKey("https://github.com/example-org/repo.git")).toBe(
      "example-org/repo",
    );
    expect(repoKey("github.com/example-org/repo/")).toBe("example-org/repo");
    expect(repoKey("http://www.github.com/example-org/repo")).toBe(
      "example-org/repo",
    );
  });

  it("finds a repository typed in another form", () => {
    const text = "https://github.com/Example-Org/Repo\nother/thing";
    expect(hasRepo(text, "example-org/repo")).toBe(true);
    expect(hasRepo(text, "example-org/else")).toBe(false);
    expect(hasRepo("\n\n", "a/b")).toBe(false);
  });

  it("adds a repository as a line of its own", () => {
    expect(toggleRepo("", "a/b")).toBe("a/b");
    expect(toggleRepo("x/y", "a/b")).toBe("x/y\na/b");
    expect(toggleRepo("x/y\n\n", "a/b")).toBe("x/y\na/b");
  });

  it("removes every line that names it, keeping the rest as typed", () => {
    expect(toggleRepo("a/b", "a/b")).toBe("");
    expect(toggleRepo("A/B\nx/y", "a/b")).toBe("x/y");
    expect(toggleRepo("x/y\nhttps://github.com/a/b\nz/w\na/b", "a/b")).toBe(
      "x/y\nz/w",
    );
  });
});

describe("filterRepos", () => {
  const repo = (fullName: string, description?: string): GitHubRepo => ({
    fullName,
    defaultBranch: "main",
    private: false,
    description,
  });
  const items = [
    repo("example-org/web-api"),
    repo("example-org/api"),
    repo("octo-user/notes", "API notes"),
    repo("octo-user/dotfiles"),
  ];

  it("returns everything for an empty query", () => {
    expect(filterRepos(items, " ")).toEqual({ shown: items, more: 0 });
  });

  it("puts names that start with the query first", () => {
    expect(filterRepos(items, "API").shown.map((r) => r.fullName)).toEqual([
      "example-org/api",
      "example-org/web-api",
      "octo-user/notes",
    ]);
    expect(filterRepos(items, "octo").shown).toHaveLength(2);
  });

  it("bounds what it shows and counts the rest", () => {
    const many = Array.from({ length: 7 }, (_, i) => repo(`o/r${i}`));
    expect(filterRepos(many, "", 5)).toEqual({
      shown: many.slice(0, 5),
      more: 2,
    });
  });
});
