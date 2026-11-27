import { fireEvent, render, screen, within } from "@testing-library/react";

import type { Project, SessionSummary } from "../api/types.gen";
import { SearchPalette } from "./SearchPalette";
import { Sidebar } from "./Sidebar";

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

const projects: Project[] = [
  {
    id: "p1",
    name: "riptide",
    repos: [],
    sandbox: { state: "warm" },
    createdAt: "2026-11-01T00:00:00Z",
  },
  {
    id: "p2",
    name: "harbor",
    repos: [],
    sandbox: { state: "warm" },
    createdAt: "2026-11-01T00:00:00Z",
  },
];

const sessions = [
  s("a", "Fix flaky migration test", "2026-11-26T10:00:00Z", {
    status: "approval",
  }),
  s("b", "Persist sidebar width", "2026-11-27T10:00:00Z", {
    projectId: "p2",
    provider: "opencode",
    repos: ["harbor", "toolkit"],
  }),
  s("c", "Tidy imports", "2026-11-20T10:00:00Z", { branch: "fix/migration" }),
];

function palette(over: Partial<Parameters<typeof SearchPalette>[0]> = {}) {
  const onSelect = vi.fn();
  const onClose = vi.fn();
  render(
    <SearchPalette
      sessions={sessions}
      projects={projects}
      onSelect={onSelect}
      onClose={onClose}
      {...over}
    />,
  );
  return { onSelect, onClose };
}

const input = () => screen.getByRole("combobox", { name: "Search sessions" });
const options = () => screen.queryAllByRole("option");

describe("SearchPalette", () => {
  it("is a labelled modal dialog with the recent sessions first", () => {
    palette();
    const dialog = screen.getByRole("dialog", { name: "Search sessions" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(input()).toHaveFocus();
    expect(screen.getByText("Recent sessions")).toBeInTheDocument();
    const opts = options();
    expect(opts.map((o) => o.textContent)).toEqual([
      expect.stringContaining("Persist sidebar width"),
      expect.stringContaining("Fix flaky migration test"),
      expect.stringContaining("Tidy imports"),
    ]);
    expect(opts[0]).toHaveAttribute("aria-selected", "true");
    expect(input()).toHaveAttribute("aria-activedescendant", opts[0].id);
    expect(within(opts[0]).getByText("HA")).toBeInTheDocument();
    expect(within(opts[0]).getByText("OpenCode")).toBeInTheDocument();
    expect(within(opts[1]).getByText("Approval")).toBeInTheDocument();
  });

  it("filters by every term, marks the matches and puts titles first", () => {
    palette();
    fireEvent.change(input(), { target: { value: "MIGRATION" } });
    expect(screen.getByText("2 matches")).toBeInTheDocument();
    const opts = options();
    expect(opts).toHaveLength(2);
    expect(opts[0]).toHaveTextContent("Fix flaky migration test");
    expect(opts[1]).toHaveTextContent("Tidy imports");
    const marks = opts[0].querySelectorAll("mark");
    expect(Array.from(marks).map((m) => m.textContent)).toEqual(["migration"]);
    expect(opts[1].querySelector("mark")?.textContent).toBe("migration");

    fireEvent.change(input(), { target: { value: "harbor width" } });
    expect(screen.getByText("1 match")).toBeInTheDocument();
    fireEvent.change(input(), { target: { value: "harbor nothing" } });
    expect(options()).toHaveLength(0);
    expect(screen.getByRole("status")).toHaveTextContent("No session matches.");
    expect(input()).not.toHaveAttribute("aria-activedescendant");
    expect(input()).toHaveAttribute("aria-expanded", "false");
  });

  it("moves with the arrows, wraps, and opens with Enter", () => {
    const { onSelect, onClose } = palette();
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    expect(options()[1]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(input(), { key: "ArrowUp" });
    fireEvent.keyDown(input(), { key: "ArrowUp" });
    expect(options()[2]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    expect(options()[0]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith("a");
    expect(onClose).toHaveBeenCalled();
  });

  it("does nothing on arrows or Enter without results", () => {
    const { onSelect } = palette({ sessions: [] });
    expect(screen.getByRole("status")).toHaveTextContent("No sessions yet.");
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("opens a session on click and follows the pointer", () => {
    const { onSelect } = palette();
    fireEvent.mouseMove(options()[2]);
    expect(options()[2]).toHaveAttribute("aria-selected", "true");
    fireEvent.click(options()[1]);
    expect(onSelect).toHaveBeenCalledWith("a");
  });

  it("closes on Escape and on a click beside it, not inside", () => {
    const { onClose } = palette();
    fireEvent.mouseDown(screen.getByRole("dialog"));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(screen.getByRole("dialog").parentElement!);
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(input(), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("keeps focus in the input on a click inside the dialog", () => {
    palette();
    // fireEvent returns false when the default (moving focus) is prevented.
    expect(fireEvent.mouseDown(screen.getByText("Recent sessions"))).toBe(
      false,
    );
    expect(fireEvent.mouseDown(screen.getByText("Esc"))).toBe(false);
    expect(fireEvent.mouseDown(options()[0])).toBe(false);
    expect(fireEvent.mouseDown(input())).toBe(true);
  });

  it("keeps Tab inside the dialog", () => {
    palette();
    fireEvent.keyDown(input(), { key: "Tab" });
    expect(input()).toHaveFocus();
    fireEvent.keyDown(input(), { key: "Tab", shiftKey: true });
    expect(input()).toHaveFocus();
  });

  it("sizes the list to its content, clamped to 60% of the viewport", () => {
    const measure = vi.fn(() => 300);
    palette({ measure });
    const body = document.querySelector<HTMLElement>(".pal-body")!;
    expect(body.style.height).toBe("300px");
    measure.mockReturnValue(5000);
    fireEvent.change(input(), { target: { value: "i" } });
    expect(body.style.height).toBe(`${Math.round(window.innerHeight * 0.6)}px`);
    measure.mockReturnValue(120);
    fireEvent(window, new Event("resize"));
    expect(body.style.height).toBe("120px");
  });
});

describe("Sidebar search", () => {
  function sidebar() {
    const onSelect = vi.fn();
    render(
      <>
        <button type="button">elsewhere</button>
        <Sidebar
          sessions={sessions}
          projects={projects}
          onSelect={onSelect}
          loaded
          error={null}
        />
      </>,
    );
    return { onSelect };
  }

  it("opens from the button and gives focus back to it", () => {
    const { onSelect } = sidebar();
    const button = screen.getByRole("button", { name: "Search sessions" });
    button.focus();
    fireEvent.click(button);
    expect(input()).toHaveFocus();
    fireEvent.change(input(), { target: { value: "tidy" } });
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith("c");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(button).toHaveFocus();
  });

  it("opens and closes with Cmd+K or Ctrl+K from anywhere", () => {
    sidebar();
    const elsewhere = screen.getByRole("button", { name: "elsewhere" });
    elsewhere.focus();
    fireEvent.keyDown(elsewhere, { key: "k", metaKey: true });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    fireEvent.keyDown(input(), { key: "k", ctrlKey: true });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(elsewhere).toHaveFocus();
    fireEvent.keyDown(elsewhere, { key: "k" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows each row's project badge and agent pill", () => {
    sidebar();
    const row = screen.getByRole("button", { name: /Persist sidebar width/ });
    expect(within(row).getByText("HA")).toBeInTheDocument();
    expect(within(row).getByText("· 2 repos")).toBeInTheDocument();
    expect(within(row).getByText("OC")).toHaveAttribute("aria-hidden", "true");
    expect(within(row).getByTitle("OpenCode")).toHaveClass("prov-oc");
    const other = screen.getByRole("button", { name: /Fix flaky/ });
    expect(within(other).getByText("RI")).toBeInTheDocument();
    expect(within(other).queryByText(/repo/)).toBeNull();
    expect(within(other).getByTitle("Claude Code")).toHaveClass("prov-cc");
  });
});
