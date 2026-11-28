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
}: {
  start?: ChosenRepo[];
  onDisconnected?: () => void;
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

  it("asks the server as typing pauses and offers to add a name it does not list", async () => {
    const calls = routeFetch({ "GET /api/github/repos": filtered(items) });
    render(<Harness />);
    await openPicker();
    fireEvent.change(search(), { target: { value: "si" } });
    fireEvent.change(search(), { target: { value: "sit" } });
    await wait(SEARCH_DELAY_MS + 10);
    expect(calls.map((c) => c.url)).toEqual([
      "/api/github/repos",
      "/api/github/repos?query=sit",
    ]);
    expect(options().map((o) => o.textContent)).toEqual(["octo/site"]);
    fireEvent.change(search(), { target: { value: "nothing" } });
    await wait(SEARCH_DELAY_MS + 10);
    expect(
      screen.getByText(en.repoNoMatch.replace("{{query}}", "nothing")),
    ).toBeInTheDocument();
    // Before the server answered for the new text: neither "no match" nor
    // an offer to add by name, though the old answer listed nothing.
    fireEvent.change(search(), { target: { value: "octo/app" } });
    expect(screen.queryByText(/No repository matches/)).toBeNull();
    expect(screen.queryByRole("option", { name: /^Add / })).toBeNull();
    await wait(SEARCH_DELAY_MS + 10);
    // Listed: no offer to add it by name, and it keeps its lock.
    expect(screen.queryByRole("option", { name: /^Add / })).toBeNull();
    fireEvent.keyDown(search(), { key: "Enter" });
    expect(chosen()).toBe("octo/app*");
    fireEvent.change(search(), {
      target: { value: "https://github.com/cli/cli" },
    });
    await wait(SEARCH_DELAY_MS + 10);
    const add = screen.getByRole("option", { name: "Add cli/cli" });
    fireEvent.click(add);
    expect(chosen()).toBe("octo/app*,cli/cli");
    expect(search()).toHaveValue("");
  });

  it("says when the list is longer than it shows, or cut by the API", async () => {
    const many = Array.from({ length: 60 }, (_, i) => repo(`o/r${i}`));
    routeFetch({ "GET /api/github/repos": filtered(many) });
    const view = render(<Harness />);
    await openPicker();
    expect(options()).toHaveLength(50);
    expect(
      screen.getByText("Showing the first 50 of 60. Type to narrow."),
    ).toBeInTheDocument();
    view.unmount();
    const capped = Array.from({ length: 1000 }, (_, i) => repo(`o/r${i}`));
    routeFetch({ "GET /api/github/repos": filtered(capped) });
    render(<Harness />);
    await openPicker();
    expect(
      screen.getByText("GitHub lists at most 1'000. Type to narrow."),
    ).toBeInTheDocument();
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
