import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";

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

  it("keeps a seen failed session's status word and colour; only the bold title goes", () => {
    const failed = (unseen: boolean) =>
      s({
        id: `f${unseen}`,
        title: `Broke ${unseen}`,
        status: "failed",
        unseen,
      });
    const { container } = renderSidebar({
      sessions: [failed(true), failed(false)],
    });
    for (const unseen of [true, false]) {
      const row = screen.getByRole("button", {
        name: new RegExp(`Broke ${unseen}`),
      });
      expect(row.classList.contains("unseen")).toBe(unseen);
      const word = within(row).getByText(en.statusFailed);
      expect(word).toHaveClass("status", "st-failed");
      expect(row.querySelector(".s-time")).toBeNull();
    }
    expect(container.querySelectorAll(".srow.unseen")).toHaveLength(1);
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
      .map((h) => h.querySelector(".nm")?.textContent ?? h.textContent);
    expect(heads).toEqual(["alpha", "beta", `${en.otherProjects}1`]);
    fireEvent.keyDown(screen.getByRole("radio", { name: en.groupByProject }), {
      key: "ArrowLeft",
    });
    expect(props.onGrouping).toHaveBeenLastCalledWith("status");
  });

  it("lists projects without sessions by project, each with its +", () => {
    const { props } = renderSidebar({
      sessions: [sessions[4]],
      grouping: "project",
      projects: [
        proj("pa", "alpha"),
        proj("pb", "beta"),
        // A setup that runs (busy; one without it was left behind).
        { ...proj("pc", "gamma"), sandbox: { state: "setup", busy: true } },
      ],
    });
    // beta has a session: its own section, its dot named.
    const beta = screen.getByRole("region", { name: "beta" });
    expect(within(beta).getByRole("img", { name: "Sandbox warm" })).toHaveClass(
      "dot-warm",
    );
    fireEvent.click(
      within(beta).getByRole("button", { name: "New session in beta" }),
    );
    expect(props.onNewSession).toHaveBeenLastCalledWith("pb");
    // alpha and gamma: one line each under "No sessions yet".
    const idle = screen.getByRole("region", { name: en.noSessionsYet });
    expect(within(idle).getAllByRole("listitem")).toHaveLength(2);
    expect(
      within(idle).getByRole("img", { name: "Sandbox setting up" }),
    ).toHaveClass("dot-busy");
    fireEvent.click(
      within(idle).getByRole("button", { name: "New session in gamma" }),
    );
    expect(props.onNewSession).toHaveBeenLastCalledWith("pc");
    expect(props.onNewSession).toHaveBeenCalledTimes(2);
    // A click on the line itself starts one too.
    fireEvent.click(within(idle).getByText("alpha"));
    expect(props.onNewSession).toHaveBeenLastCalledWith("pa");
  });

  it("names a setup left behind as the card does", () => {
    renderSidebar({
      sessions: [],
      grouping: "project",
      projects: [{ ...proj("pc", "gamma"), sandbox: { state: "setup" } }],
    });
    expect(
      screen.getByRole("img", { name: "Sandbox setup did not finish" }),
    ).toHaveClass("dot-failed");
  });

  it("keeps the dot and the + out of the heading, the project out of its rows", () => {
    renderSidebar({ grouping: "project" });
    const alpha = screen.getByRole("region", { name: "alpha" });
    expect(
      within(alpha).getByRole("heading", { level: 2 }),
    ).toHaveAccessibleName(/^alpha\s*3$/);
    const head = within(alpha).getByRole("heading", { level: 2 });
    expect(within(head).queryByRole("button")).not.toBeInTheDocument();
    expect(within(head).queryByRole("img")).not.toBeInTheDocument();
    const row = within(alpha).getByRole("button", { name: /Needs an answer/ });
    expect(row).toHaveClass("in-proj");
    expect(row).not.toHaveTextContent("alpha");
    // By status the rows keep their project.
    cleanup();
    renderSidebar();
    expect(
      screen.getByRole("button", { name: /Needs an answer/ }),
    ).toHaveTextContent("alpha");
  });

  it("lists projects without sessions inert when nothing can start", () => {
    renderSidebar({
      sessions: [],
      grouping: "project",
      onNewSession: undefined,
    });
    const row = screen.getByText("alpha").closest("li");
    expect(row).not.toHaveClass("can-add");
    expect(screen.queryByRole("button", { name: /New session in/ })).toBeNull();
  });

  it("says nothing about projects when the session list failed", () => {
    renderSidebar({
      sessions: [],
      projects: [],
      grouping: "project",
      error: "internal",
    });
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText(en.noProjects)).not.toBeInTheDocument();
    expect(screen.queryByText(en.empty)).not.toBeInTheDocument();
  });

  it("closes the drawer after a + on narrow screens", () => {
    const { props } = renderSidebar({
      sessions: [],
      grouping: "project",
      narrow: true,
      drawerOpen: true,
    });
    fireEvent.click(
      screen.getByRole("button", { name: "New session in alpha" }),
    );
    expect(props.onNewSession).toHaveBeenCalledWith("pa");
    expect(props.onCloseDrawer).toHaveBeenCalled();
  });

  it("points to the projects page without any project", () => {
    const onOpenProjects = vi.fn();
    renderSidebar({
      sessions: [],
      projects: [],
      grouping: "project",
      onOpenProjects,
    });
    expect(screen.getByText(en.noProjects)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: en.addProject }));
    expect(onOpenProjects).toHaveBeenCalled();
    cleanup();
    // A failed project list is not "no project".
    renderSidebar({
      sessions: [],
      projects: [],
      grouping: "project",
      projectsError: "internal",
    });
    expect(screen.queryByText(en.noProjects)).not.toBeInTheDocument();
    expect(screen.getByText(en.empty)).toBeInTheDocument();
  });

  it("starts a plain new session from New session, no project given", () => {
    const { props } = renderSidebar({ sessions: [] });
    expect(screen.getAllByText(en.empty)).toHaveLength(4);
    fireEvent.click(screen.getByRole("button", { name: en.newSession }));
    expect(props.onNewSession).toHaveBeenCalledWith(undefined);
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
    const search = screen.getByRole("button", { name: en.search });
    expect(search).toHaveFocus();
    expect(close).not.toHaveAttribute("aria-pressed");

    // Tab from the last control wraps to the first, Shift+Tab back.
    const account = screen.getByRole("button", { name: "Account: owner" });
    account.focus();
    fireEvent.keyDown(account, { key: "Tab" });
    expect(search).toHaveFocus();
    fireEvent.keyDown(search, { key: "Tab", shiftKey: true });
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

  // Owner decision: no search field; a magnifier icon beside the wordmark and
  // the collapse button, and Cmd/Ctrl+K.
  it("opens the search from an icon button at the top and from Cmd+K", () => {
    const { container } = renderSidebar();
    const search = screen.getByRole("button", { name: en.search });
    expect(search).toHaveClass("icon-btn");
    expect(search).toHaveTextContent("");
    expect(search.closest(".sb-top")).not.toBeNull();
    expect(screen.getAllByRole("button", { name: en.search })).toHaveLength(1);
    expect(container.querySelector(".sb-tools kbd")).toBeNull();
    fireEvent.click(search);
    expect(
      screen.getByPlaceholderText(en.searchPlaceholder),
    ).toBeInTheDocument();
    fireEvent.keyDown(window, { key: "k", metaKey: true });
    expect(screen.queryByPlaceholderText(en.searchPlaceholder)).toBeNull();
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(
      screen.getByPlaceholderText(en.searchPlaceholder),
    ).toBeInTheDocument();
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
