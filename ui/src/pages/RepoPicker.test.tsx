import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";

import type { GitHubRepo } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { json, routeFetch, type FetchCall } from "../test/routeFetch";
import { RepoPicker, SEARCH_DELAY_MS, type ChosenRepo } from "./RepoPicker";

const en = resources.en.projects;
const errors = resources.en.errors;

const repo = (
  fullName: string,
  locked = false,
  description?: string,
): GitHubRepo => ({
  fullName,
  defaultBranch: "main",
  private: locked,
  description,
});

const items = [
  repo("octo/app", true, "The app"),
  repo("octo/site"),
  repo("octo/docs", false, "Docs"),
];

// filtered answers the list as the server would: by name.
const filtered = (all: GitHubRepo[]) => (c: FetchCall) => {
  const q = new URL(c.url, "http://x").searchParams.get("query") ?? "";
  return json(200, { items: all.filter((r) => r.fullName.includes(q)) });
};

function Harness({
  start = [],
  onDisconnected = () => {},
  onFailed,
}: {
  start?: ChosenRepo[];
  onDisconnected?: () => void;
  onFailed?: (failed: boolean) => void;
}) {
  const [chosen, setChosen] = useState(start);
  return (
    <>
      <span id="lbl">{en.repos}</span>
      <RepoPicker
        chosen={chosen}
        onAdd={(r) => setChosen((c) => [...c, r])}
        onRemove={(f) => setChosen((c) => c.filter((x) => x.fullName !== f))}
        onDisconnected={onDisconnected}
        onFailed={onFailed}
        labelId="lbl"
      />
      <output data-testid="chosen">
        {chosen.map((c) => `${c.fullName}${c.locked ? "*" : ""}`).join(",")}
      </output>
    </>
  );
}

const chosen = () => screen.getByTestId("chosen").textContent;
const trigger = () => screen.getByRole("button", { expanded: false });
const search = () => screen.getByRole("combobox", { name: en.repoSearch });
const options = () => screen.queryAllByRole("option");
const wait = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

async function openPicker() {
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", {
        name: /Choose repositories|Add a repository/,
      }),
    );
  });
  await wait(1);
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("RepoPicker", () => {
  it("lists in the API's order with a lock only on the ones not public", async () => {
    const calls = routeFetch({ "GET /api/github/repos": filtered(items) });
    render(<Harness />);
    expect(trigger()).toHaveAccessibleName(`${en.repos} ${en.pickRepos}`);
    await openPicker();
    expect(calls[0].url).toBe("/api/github/repos");
    expect(options().map((o) => o.textContent)).toEqual([
      "octo/appThe app",
      "octo/site",
      "octo/docsDocs",
    ]);
    const locks = screen.getAllByRole("img", { name: en.repoNotPublic });
    expect(locks).toHaveLength(1);
    expect(options()[0]).toContainElement(locks[0]);
    expect(search()).toHaveFocus();
    expect(screen.getByRole("listbox")).toHaveAttribute(
      "aria-multiselectable",
      "true",
    );
  });

  it("works by keyboard: arrows, Enter, Escape, Backspace", async () => {
    routeFetch({ "GET /api/github/repos": filtered(items) });
    render(<Harness />);
    const t = trigger();
    t.focus();
    await act(async () => {
      fireEvent.keyDown(t, { key: "ArrowDown" });
    });
    await wait(1);
    const s = search();
    expect(s).toHaveAttribute("aria-activedescendant", options()[0].id);
    fireEvent.keyDown(s, { key: "ArrowDown" });
    fireEvent.keyDown(s, { key: "ArrowDown" });
    expect(s).toHaveAttribute("aria-activedescendant", options()[2].id);
    fireEvent.keyDown(s, { key: "ArrowDown" });
    expect(s).toHaveAttribute("aria-activedescendant", options()[0].id);
    fireEvent.keyDown(s, { key: "ArrowUp" });
    expect(s).toHaveAttribute("aria-activedescendant", options()[2].id);
    fireEvent.keyDown(s, { key: "Home" });
    fireEvent.keyDown(s, { key: "Enter" });
    expect(chosen()).toBe("octo/app*");
    expect(options()[0]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(s, { key: "End" });
    fireEvent.keyDown(s, { key: "Enter" });
    expect(chosen()).toBe("octo/app*,octo/docs");
    // Enter on a chosen one takes it out again.
    fireEvent.keyDown(s, { key: "Enter" });
    expect(chosen()).toBe("octo/app*");
    fireEvent.keyDown(s, { key: "Backspace" });
    expect(chosen()).toBe("");
    fireEvent.change(s, { target: { value: "si" } });
    fireEvent.keyDown(s, { key: "Backspace" });
    fireEvent.keyDown(s, { key: "Escape" });
    expect(s).toHaveValue("");
    fireEvent.keyDown(s, { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("button", { expanded: false })).toHaveFocus();
  });

  it("reads the list once per opening and filters it here", async () => {
    const calls = routeFetch({ "GET /api/github/repos": filtered(items) });
    render(<Harness />);
    await openPicker();
    fireEvent.change(search(), { target: { value: "si" } });
    fireEvent.change(search(), { target: { value: "docs" } });
    await wait(SEARCH_DELAY_MS + 10);
    expect(calls.map((c) => c.url)).toEqual(["/api/github/repos"]);
    // The description counts too.
    expect(options().map((o) => o.textContent)).toEqual(["octo/docsDocs"]);
    fireEvent.change(search(), { target: { value: "nothing" } });
    expect(
      screen.getByText(en.repoNoMatch.replace("{{query}}", "nothing")),
    ).toBeInTheDocument();
    // Enter at once after typing picks what matches this text, never an
    // option of the text before.
    fireEvent.change(search(), { target: { value: "sit" } });
    fireEvent.keyDown(search(), { key: "Enter" });
    expect(chosen()).toBe("octo/site");
    // Listed: no offer to add it by name, and it keeps its lock.
    fireEvent.change(search(), { target: { value: "octo/app" } });
    expect(screen.queryByRole("option", { name: /^Add / })).toBeNull();
    fireEvent.keyDown(search(), { key: "Enter" });
    expect(chosen()).toBe("octo/site,octo/app*");
    fireEvent.change(search(), {
      target: { value: "https://github.com/cli/cli" },
    });
    fireEvent.click(screen.getByRole("option", { name: "Add cli/cli" }));
    expect(chosen()).toBe("octo/site,octo/app*,cli/cli");
    expect(search()).toHaveValue("");
    // A new opening reads it again.
    fireEvent.keyDown(search(), { key: "Escape" });
    await openPicker();
    expect(calls).toHaveLength(2);
  });

  it("shows one the server would refuse, with why, and does not add it", async () => {
    routeFetch({
      "GET /api/github/repos": json(200, {
        items: [repo("octo/.github"), repo("fork/app"), repo("octo/app")],
      }),
    });
    render(<Harness start={[{ fullName: "octo/app", locked: false }]} />);
    await openPicker();
    const [dot, fork] = options();
    expect(dot).toHaveAttribute("aria-disabled", "true");
    expect(dot).toHaveTextContent(en.repoBadDir);
    expect(fork).toHaveTextContent(
      en.repoSameDir.replace("{{name}}", "octo/app"),
    );
    fireEvent.click(dot);
    fireEvent.click(fork);
    expect(chosen()).toBe("octo/app");
    // The chosen one itself stays removable.
    fireEvent.click(options()[2]);
    expect(chosen()).toBe("");
    // By name too.
    fireEvent.change(search(), { target: { value: "x/_private-notes" } });
    const add = screen.getByRole("option", { name: /Add x\/_private-notes/ });
    expect(add).toHaveAttribute("aria-disabled", "true");
    fireEvent.keyDown(search(), { key: "Enter" });
    expect(chosen()).toBe("");
  });

  it("searches a capped list on the server, past what was read", async () => {
    const capped = Array.from({ length: 1000 }, (_, i) =>
      repo(`o/r${String(i).padStart(4, "0")}`),
    );
    const far = repo("o/far-away");
    const calls = routeFetch({
      "GET /api/github/repos": (c) =>
        c.url.includes("query=")
          ? filtered([...capped, far])(c)
          : json(200, { items: capped }),
    });
    render(<Harness />);
    await openPicker();
    expect(
      screen.getByText(
        "GitHub listed the first 1'000. Type to search all of them.",
      ),
    ).toBeInTheDocument();
    expect(options()).toHaveLength(50);
    fireEvent.change(search(), { target: { value: "far" } });
    // Until the server answers: what was read, filtered; nothing matches,
    // and Enter picks nothing (no option of another text).
    expect(screen.getByText(en.repoSearching)).toBeInTheDocument();
    expect(screen.queryByText(/No repository matches/)).toBeNull();
    fireEvent.keyDown(search(), { key: "Enter" });
    expect(chosen()).toBe("");
    await wait(SEARCH_DELAY_MS + 10);
    expect(calls.map((c) => c.url)).toEqual([
      "/api/github/repos",
      "/api/github/repos?query=far",
    ]);
    expect(options().map((o) => o.textContent)).toEqual(["o/far-away"]);
    // A search the server cut too says so.
    fireEvent.change(search(), { target: { value: "o/r" } });
    await wait(SEARCH_DELAY_MS + 10);
    expect(
      screen.getByText("Showing the first 1'000 matches. Type more to narrow."),
    ).toBeInTheDocument();
  });

  it("says when the list is longer than it shows", async () => {
    const many = Array.from({ length: 60 }, (_, i) => repo(`o/r${i}`));
    routeFetch({ "GET /api/github/repos": filtered(many) });
    render(<Harness />);
    await openPicker();
    expect(options()).toHaveLength(50);
    expect(
      screen.getByText("Showing the first 50 of 60. Type to narrow."),
    ).toBeInTheDocument();
  });

  it("stops at the search when Backspace is held", async () => {
    routeFetch({ "GET /api/github/repos": json(200, { items }) });
    render(<Harness start={[{ fullName: "a/one", locked: false }]} />);
    await openPicker();
    fireEvent.keyDown(search(), { key: "Backspace", repeat: true });
    expect(chosen()).toBe("a/one");
    fireEvent.keyDown(search(), { key: "Backspace" });
    expect(chosen()).toBe("");
  });

  it("fails a capped list's search with a retry", async () => {
    const capped = Array.from({ length: 1000 }, (_, i) => repo(`o/r${i}`));
    routeFetch({
      "GET /api/github/repos": [
        json(200, { items: capped }),
        json(502, { code: "upstream" }),
        json(200, { items: [repo("o/r7")] }),
      ],
    });
    const failed = vi.fn();
    render(<Harness onFailed={failed} />);
    await openPicker();
    fireEvent.change(search(), { target: { value: "r7" } });
    await wait(SEARCH_DELAY_MS + 10);
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
    expect(failed).toHaveBeenLastCalledWith(true);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.repoRetry }));
    });
    await wait(1);
    expect(options().map((o) => o.textContent)).toEqual(["o/r7"]);
    expect(failed).toHaveBeenLastCalledWith(false);
  });

  it("says when the connection reaches nothing", async () => {
    routeFetch({ "GET /api/github/repos": json(200, { items: [] }) });
    render(<Harness />);
    await openPicker();
    expect(screen.getByText(en.repoNone)).toBeInTheDocument();
  });

  it("shows a failed list with a retry", async () => {
    routeFetch({
      "GET /api/github/repos": [
        json(502, { code: "upstream" }),
        json(200, { items }),
      ],
    });
    render(<Harness />);
    await openPicker();
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.repoRetry }));
    });
    await wait(1);
    expect(options()).toHaveLength(3);
  });

  it("hands a lost connection to the form", async () => {
    routeFetch({
      "GET /api/github/repos": json(409, { code: "github_not_connected" }),
    });
    const lost = vi.fn();
    render(<Harness onDisconnected={lost} />);
    await openPicker();
    expect(lost).toHaveBeenCalled();
  });

  it("removes chips by their button and by Backspace on the trigger", async () => {
    routeFetch({ "GET /api/github/repos": json(200, { items }) });
    render(
      <Harness
        start={[
          { fullName: "a/one", locked: true },
          { fullName: "a/two", locked: false },
        ]}
      />,
    );
    const list = screen.getByRole("list", { name: en.repoChosen });
    expect(
      within(list).getAllByRole("img", { name: en.repoNotPublic }),
    ).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Remove a/one" }));
    expect(chosen()).toBe("a/two");
    fireEvent.keyDown(
      screen.getByRole("button", { name: `${en.repos} ${en.pickMore}` }),
      { key: "Backspace" },
    );
    expect(chosen()).toBe("");
  });

  it("closes on a click outside and by its trigger", async () => {
    routeFetch({ "GET /api/github/repos": json(200, { items }) });
    render(<Harness />);
    await openPicker();
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("listbox")).toBeNull();
    await openPicker();
    fireEvent.mouseMove(options()[1]);
    expect(search()).toHaveAttribute("aria-activedescendant", options()[1].id);
    fireEvent.keyDown(search(), { key: "Tab" });
    expect(screen.queryByRole("listbox")).toBeNull();
    await openPicker();
    fireEvent.click(screen.getByRole("button", { expanded: true }));
    expect(screen.queryByRole("listbox")).toBeNull();
  });
});
