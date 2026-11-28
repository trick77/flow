import { fireEvent, render, screen, within } from "@testing-library/react";

import type { Project, SessionSummary } from "../api/types.gen";
import { formatNumber } from "../i18n/format";
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

  it("finds projects to start a session in, ones without sessions too", () => {
    const onNewSession = vi.fn();
    const withEmpty = [
      ...projects,
      { ...projects[0], id: "p3", name: "ovh-restock" },
    ];
    const { onClose, onSelect } = palette({
      projects: withEmpty,
      onNewSession,
    });
    // No query: sessions only.
    expect(screen.queryByText("Projects")).not.toBeInTheDocument();
    fireEvent.change(input(), { target: { value: "ovh" } });
    expect(screen.queryByText("No session matches.")).not.toBeInTheDocument();
    const opt = screen.getByRole("option", {
      name: "New session in ovh-restock",
    });
    expect(opt.querySelector("mark")?.textContent).toBe("ovh");
    expect(opt).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(onNewSession).toHaveBeenCalledWith("p3");
    expect(onSelect).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it("lists sessions then matching projects, arrows over both", () => {
    const onNewSession = vi.fn();
    palette({ onNewSession });
    fireEvent.change(input(), { target: { value: "harbor" } });
    const opts = options();
    expect(opts).toHaveLength(2);
    expect(opts[0]).toHaveTextContent("Persist sidebar width");
    expect(opts[1]).toHaveAccessibleName("New session in harbor");
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    expect(opts[1]).toHaveAttribute("aria-selected", "true");
    fireEvent.click(opts[1]);
    expect(onNewSession).toHaveBeenCalledWith("p2");
  });

  it("lists every project while there is no session", () => {
    palette({ sessions: [], onNewSession: vi.fn() });
    expect(screen.getByRole("status")).toHaveTextContent("No sessions yet.");
    expect(options().map((o) => o.getAttribute("aria-label"))).toEqual([
      "New session in riptide",
      "New session in harbor",
    ]);
  });

  it("counts every match when it lists only the first ones", () => {
    const many = Array.from({ length: 1203 }, (_, i) =>
      s(`m${i}`, `flow task ${i}`, "2026-11-01T00:00:00Z"),
    );
    palette({ sessions: many });
    fireEvent.change(input(), { target: { value: "flow" } });
    expect(
      screen.getByText(`First 50 of ${formatNumber(1203)} matches`),
    ).toBeInTheDocument();
    expect(options()).toHaveLength(50);
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

  it("ignores the repeats of a held shortcut", () => {
    sidebar();
    const elsewhere = screen.getByRole("button", { name: "elsewhere" });
    fireEvent.keyDown(elsewhere, { key: "k", metaKey: true });
    const repeat = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      repeat: true,
      bubbles: true,
      cancelable: true,
    });
    expect(fireEvent(input(), repeat)).toBe(false);
    expect(fireEvent(input(), repeat)).toBe(false);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("sets an unseen session's title in bold", () => {
    render(
      <Sidebar
        sessions={[{ ...sessions[2], unseen: true }, sessions[1]]}
        projects={projects}
        onSelect={vi.fn()}
        loaded
        error={null}
      />,
    );
    const unseen = screen.getByRole("button", { name: /Tidy imports/ });
    expect(unseen).toHaveClass("srow", "unseen");
    expect(unseen).toHaveAttribute("aria-description", "New activity");
    const seen = screen.getByRole("button", { name: /Persist sidebar/ });
    expect(seen).not.toHaveClass("unseen");
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
