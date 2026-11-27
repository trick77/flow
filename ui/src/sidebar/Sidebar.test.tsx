import { fireEvent, render, screen, within } from "@testing-library/react";

import type { Project, SessionSummary } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { AccountContext } from "../shell/AuthGate";
import { Sidebar } from "./Sidebar";

const en = resources.en.sidebar;

const NOW = new Date("2026-11-27T14:00:00Z");
const ago = (min: number) =>
  new Date(NOW.getTime() - min * 60_000).toISOString();

const proj = (id: string, name: string): Project => ({
  id,
  name,
  repos: [],
  sandbox: { state: "warm" },
  createdAt: ago(9000),
});

const s = (over: Partial<SessionSummary>): SessionSummary => ({
  id: "s",
  projectId: "pa",
  title: "t",
  provider: "claude",
  model: "m",
  mode: "ask",
  status: "idle",
  unseen: false,
  repos: ["a"],
  branch: "b",
  lastActivityAt: ago(1),
  createdAt: ago(9000),
  ...over,
});

const sessions = [
  s({ id: "ask", title: "Needs an answer", status: "question" }),
  s({ id: "new", title: "Done unseen", status: "done", unseen: true }),
  s({
    id: "seen",
    title: "Tidy seen",
    status: "done",
    lastActivityAt: ago(125),
  }),
  s({
    id: "lost",
    title: "Orphan",
    projectId: "gone",
    lastActivityAt: ago(3 * 1440),
  }),
  s({ id: "b1", title: "Beta work", projectId: "pb", status: "working" }),
];
const projects = [proj("pa", "alpha"), proj("pb", "beta")];

function renderSidebar(over: Partial<Parameters<typeof Sidebar>[0]> = {}) {
  const props = {
    sessions,
    projects,
    onSelect: vi.fn(),
    onNewSession: vi.fn(),
    loaded: true,
    error: null,
    onGrouping: vi.fn(),
    onRail: vi.fn(),
    onCloseDrawer: vi.fn(),
    onOpenSettings: vi.fn(),
    ...over,
  };
  const view = render(
    <AccountContext.Provider value={{ name: "owner", signOut: vi.fn() }}>
      <Sidebar {...props} />
    </AccountContext.Provider>,
  );
  return { ...view, props };
}

describe("Sidebar", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("groups by status: done only while unseen, idle rows with their time", () => {
    renderSidebar();
    const sec = (name: string) => screen.getByRole("region", { name });
    expect(within(sec(en.sectionDone)).getAllByRole("button")).toHaveLength(1);
    const idle = sec(en.sectionIdle);
    const seen = within(idle).getByRole("button", { name: /Tidy seen/ });
    expect(seen).toHaveTextContent("2h");
    expect(seen).not.toHaveTextContent(en.statusDone);
    expect(
      within(idle).getByRole("button", { name: /Orphan/ }),
    ).toHaveTextContent("3d");
    expect(within(sec(en.sectionNeeds)).getByRole("button")).toHaveTextContent(
      en.statusQuestion,
    );
  });

  it("switches the grouping with clicks and arrow keys", () => {
    const { props, rerender } = renderSidebar();
    const group = screen.getByRole("radiogroup", { name: en.groupLabel });
    const byStatus = within(group).getByRole("radio", {
      name: en.groupByStatus,
    });
    const byProject = within(group).getByRole("radio", {
      name: en.groupByProject,
    });
    expect(byStatus).toHaveAttribute("aria-checked", "true");
    expect(byProject).toHaveAttribute("tabindex", "-1");
    fireEvent.click(byProject);
    expect(props.onGrouping).toHaveBeenLastCalledWith("project");
    fireEvent.keyDown(byStatus, { key: "ArrowRight" });
    expect(props.onGrouping).toHaveBeenLastCalledWith("project");
    expect(byProject).toHaveFocus();
    fireEvent.keyDown(byStatus, { key: "Enter" });
    expect(props.onGrouping).toHaveBeenCalledTimes(2);

    rerender(<Sidebar {...props} grouping="project" />);
    const heads = screen
      .getAllByRole("heading", { level: 2 })
      .map((h) => h.firstChild?.textContent);
    expect(heads).toEqual(["alpha", "beta", en.otherProjects]);
    fireEvent.keyDown(screen.getByRole("radio", { name: en.groupByProject }), {
      key: "ArrowLeft",
    });
    expect(props.onGrouping).toHaveBeenLastCalledWith("status");
  });

  it("says nothing is there when grouped by project without sessions", () => {
    renderSidebar({ sessions: [], grouping: "project" });
    expect(screen.getByText(en.empty)).toBeInTheDocument();
  });

  it("collapses to the rail: icons, sessions with status dots, the account", () => {
    const { props, rerender } = renderSidebar();
    fireEvent.click(screen.getByRole("button", { name: en.collapse }));
    expect(props.onRail).toHaveBeenCalledWith(true);

    rerender(
      <AccountContext.Provider value={{ name: "owner", signOut: vi.fn() }}>
        <Sidebar {...props} rail selectedId="ask" newSessionOpen />
      </AccountContext.Provider>,
    );
    const expand = screen.getByRole("button", { name: en.expand });
    expect(expand).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByRole("navigation", { name: en.listLabel })).toBeNull();
    const rail = screen.getByRole("navigation", { name: en.railLabel });
    const items = within(rail).getAllByRole("button");
    expect(items.map((b) => b.getAttribute("aria-label"))).toEqual([
      "alpha: Needs an answer (Question)",
      "beta: Beta work (Working)",
      "alpha: Done unseen (Done)",
      "alpha: Tidy seen",
      ": Orphan",
    ]);
    expect(items[0]).toHaveAttribute("aria-current", "true");
    expect(items[0].querySelector(".rail-dot")).not.toBeNull();
    expect(items[3].querySelector(".rail-dot")).toBeNull();
    fireEvent.click(items[1]);
    expect(props.onSelect).toHaveBeenCalledWith("b1");
    expect(props.onCloseDrawer).not.toHaveBeenCalled();

    const newBtn = screen.getByRole("button", { name: en.newSession });
    expect(newBtn).toHaveAttribute("aria-current", "page");
    fireEvent.click(newBtn);
    expect(props.onNewSession).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Account: owner" }));
    expect(props.onOpenSettings).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: en.search }));
    expect(
      screen.getByPlaceholderText(en.searchPlaceholder),
    ).toBeInTheDocument();
    fireEvent.click(expand);
    expect(props.onRail).toHaveBeenLastCalledWith(false);
  });

  it("is a modal drawer on narrow screens: focus inside, Tab kept, Escape and choices close it", () => {
    const { props, rerender, container } = renderSidebar({
      narrow: true,
      rail: true,
    });
    const aside = container.querySelector("aside") as HTMLElement;
    // Closed: out of reach, the rail ignored.
    expect(aside).toHaveAttribute("inert");
    expect(screen.queryByRole("dialog")).toBeNull();

    rerender(
      <AccountContext.Provider value={{ name: "owner", signOut: vi.fn() }}>
        <Sidebar {...props} narrow rail drawerOpen />
      </AccountContext.Provider>,
    );
    const dialog = screen.getByRole("dialog", { name: en.label });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    const close = screen.getByRole("button", { name: en.closeSidebar });
    expect(close).toHaveFocus();
    expect(close).not.toHaveAttribute("aria-pressed");

    // Tab from the last control wraps to the first, Shift+Tab back.
    const account = screen.getByRole("button", { name: "Account: owner" });
    account.focus();
    fireEvent.keyDown(account, { key: "Tab" });
    expect(close).toHaveFocus();
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(account).toHaveFocus();
    fireEvent.keyDown(account, { key: "a" });

    fireEvent.keyDown(close, { key: "Escape" });
    expect(props.onCloseDrawer).toHaveBeenCalledTimes(1);
    fireEvent.click(close);
    expect(props.onCloseDrawer).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: /Beta work/ }));
    expect(props.onSelect).toHaveBeenCalledWith("b1");
    expect(props.onCloseDrawer).toHaveBeenCalledTimes(3);
    fireEvent.click(screen.getByRole("button", { name: en.newSession }));
    expect(props.onCloseDrawer).toHaveBeenCalledTimes(4);
    fireEvent.click(account);
    fireEvent.click(
      screen.getByRole("menuitem", { name: resources.en.shell.settings }),
    );
    expect(props.onOpenSettings).toHaveBeenCalled();
    expect(props.onCloseDrawer).toHaveBeenCalledTimes(5);
  });

  it("leaves keys from the search palette to the palette", () => {
    const { props } = renderSidebar({ narrow: true, drawerOpen: true });
    fireEvent.click(screen.getByRole("button", { name: en.search }));
    const box = screen.getByPlaceholderText(en.searchPlaceholder);
    fireEvent.keyDown(box, { key: "Tab" });
    fireEvent.keyDown(box, { key: "Escape" });
    expect(props.onCloseDrawer).not.toHaveBeenCalled();
    expect(screen.queryByPlaceholderText(en.searchPlaceholder)).toBeNull();
  });
});
