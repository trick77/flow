import { fireEvent, render, screen } from "@testing-library/react";

import { resources } from "../i18n/resources";
import { Header } from "./Header";

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
});
