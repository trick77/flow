import { act, fireEvent, render, screen, within } from "@testing-library/react";

import { resources } from "../i18n/resources";
import { DrawerContext } from "./drawer";
import { Header, sandboxTone } from "./Header";

const en = resources.en;

describe("Header", () => {
  it("shows a plain title without onRename", () => {
    render(<Header project="riptide" title="Fix the test" branch="" />);
    expect(
      screen.getByRole("heading", { level: 2, name: "Fix the test" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("riptide")).toBeInTheDocument();
  });

  it("edits the title in place: Enter saves, Escape cancels", () => {
    const onRename = vi.fn();
    render(
      <Header title="Fix the test" branch="flow/fix" onRename={onRename} />,
    );
    const open = () =>
      fireEvent.click(screen.getByRole("button", { name: "Fix the test" }));
    const field = () =>
      screen.getByRole("textbox", { name: en.shell.renameLabel });

    open();
    expect(field()).toHaveFocus();
    expect(field()).toHaveValue("Fix the test");
    fireEvent.change(field(), { target: { value: "Other" } });
    fireEvent.keyDown(field(), { key: "Escape" });
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(onRename).not.toHaveBeenCalled();

    // Unchanged or empty: nothing saved.
    open();
    fireEvent.keyDown(field(), { key: "Enter" });
    open();
    fireEvent.change(field(), { target: { value: "   " } });
    fireEvent.blur(field());
    expect(onRename).not.toHaveBeenCalled();

    open();
    fireEvent.change(field(), { target: { value: " Fix the flaky test " } });
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(onRename).toHaveBeenCalledWith("Fix the flaky test");
    expect(screen.getByText("flow/fix")).toBeInTheDocument();
  });

  // A blur that arrives after Escape or Enter but before the re-render runs
  // the same render's handlers (both in one act here).
  it("finishes an edit once: no save after Escape, no second save after Enter", () => {
    const onRename = vi.fn();
    render(<Header title="T" branch="b" onRename={onRename} />);
    const field = () =>
      screen.getByRole("textbox", { name: en.shell.renameLabel });
    fireEvent.click(screen.getByRole("button", { name: "T" }));
    fireEvent.change(field(), { target: { value: "wrong name" } });
    act(() => {
      const f = field();
      fireEvent.keyDown(f, { key: "Escape" });
      fireEvent.blur(f);
    });
    expect(onRename).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "T" }));
    fireEvent.change(field(), { target: { value: "right name" } });
    act(() => {
      const f = field();
      fireEvent.keyDown(f, { key: "Enter" });
      fireEvent.blur(f);
    });
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(onRename).toHaveBeenCalledWith("right name");
  });

  it("shows the sandbox chip for every state", () => {
    expect(sandboxTone("warm")).toBe("warm");
    expect(sandboxTone("setup")).toBe("busy");
    expect(sandboxTone("deleting")).toBe("busy");
    expect(sandboxTone("stopped")).toBe("down");
    const { rerender } = render(<Header title="T" branch="b" sandbox="warm" />);
    expect(screen.getByTitle(en.shell.sandboxChipWarm)).toHaveClass(
      "tone-warm",
    );
    rerender(<Header title="T" branch="b" sandbox="failed" />);
    expect(screen.getByTitle(en.shell.sandboxChipFailed)).toHaveClass(
      "tone-down",
    );
    rerender(<Header title="T" branch="b" />);
    expect(screen.queryByTitle(en.shell.sandboxChipFailed)).toBeNull();
  });

  it("lists a multi-repository session's repositories and opens one's diff", () => {
    const onOpen = vi.fn();
    const repos = [
      { repoId: "r1", name: "org/app", added: 3, removed: 1, path: "/w/app" },
      { repoId: "r2", name: "org/lib" },
    ];
    const { rerender } = render(
      <>
        <Header title="T" branch="feat/x" repos={repos} onOpenRepo={onOpen} />
        <button type="button">outside</button>
      </>,
    );
    const chip = screen.getByRole("button", {
      name: "2 repositories in this session",
    });
    expect(chip).toHaveTextContent("2 repos");
    fireEvent.click(chip);
    const menu = screen.getByRole("menu", { name: en.shell.reposMenu });
    expect(menu).toHaveTextContent("Worktrees on feat/x");
    const [app, lib] = within(menu).getAllByRole("menuitem");
    expect(app).toHaveFocus();
    expect(app).toHaveTextContent("org/app+3 −1/w/app");
    expect(lib).toHaveTextContent(en.shell.repoNoWorktree);
    fireEvent.keyDown(app, { key: "ArrowDown" });
    expect(lib).toHaveFocus();
    fireEvent.keyDown(lib, { key: "ArrowDown" });
    expect(app).toHaveFocus();
    fireEvent.keyDown(app, { key: "ArrowUp" });
    expect(lib).toHaveFocus();
    fireEvent.keyDown(lib, { key: "x" });
    fireEvent.keyDown(lib, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    expect(chip).toHaveFocus();
    fireEvent.keyDown(chip, { key: "Escape" });

    fireEvent.click(chip);
    fireEvent.mouseDown(screen.getByRole("menu"));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.mouseDown(screen.getByRole("button", { name: "outside" }));
    expect(screen.queryByRole("menu")).toBeNull();

    fireEvent.click(chip);
    fireEvent.click(screen.getAllByRole("menuitem")[0]);
    expect(onOpen).toHaveBeenCalledWith("r1");
    expect(screen.queryByRole("menu")).toBeNull();

    // Without worktrees (names only) the items open nothing.
    rerender(
      <Header
        title="T"
        branch="feat/x"
        repos={[{ name: "app" }, { name: "lib" }]}
        onOpenRepo={onOpen}
      />,
    );
    fireEvent.click(chip);
    const item = screen.getAllByRole("menuitem")[0];
    expect(item).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(item);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(en.shell.reposHint)).toBeNull();

    // One repository: no menu.
    rerender(<Header title="T" branch="b" repos={[{ name: "app" }]} />);
    expect(screen.queryByRole("button", { name: /repositor/ })).toBeNull();
  });

  it("puts the drawer's menu button first on narrow screens", () => {
    const openDrawer = vi.fn();
    const { rerender } = render(
      <DrawerContext.Provider
        value={{ narrow: false, open: false, openDrawer }}
      >
        <Header title="T" branch="" />
      </DrawerContext.Provider>,
    );
    expect(
      screen.queryByRole("button", { name: en.shell.openSidebar }),
    ).toBeNull();
    rerender(
      <DrawerContext.Provider value={{ narrow: true, open: false, openDrawer }}>
        <Header title="T" branch="" />
      </DrawerContext.Provider>,
    );
    const menu = screen.getByRole("button", { name: en.shell.openSidebar });
    expect(menu).toHaveAttribute("aria-expanded", "false");
    expect(menu).toHaveAttribute("aria-controls", "sidebar");
    fireEvent.click(menu);
    expect(openDrawer).toHaveBeenCalledWith(menu);
  });
});
