import { fireEvent, render, screen } from "@testing-library/react";

import { Picker } from "./Picker";

function picker(onChange = vi.fn()) {
  render(
    <>
      <button type="button">outside</button>
      <Picker
        label="Fruit"
        heading="Pick one"
        value="b"
        onChange={onChange}
        options={[
          { value: "a", label: "Apple", group: "Red" },
          { value: "b", label: "Berry", description: "small", group: "Red" },
          { value: "c", label: "Cherry", disabled: true, group: "Dark" },
          { value: "d", label: "Date", group: "Dark" },
        ]}
      >
        Berry
      </Picker>
    </>,
  );
  return onChange;
}

const button = () => screen.getByRole("button", { name: "Fruit" });
const item = (name: RegExp) => screen.getByRole("menuitemradio", { name });

describe("Picker", () => {
  it("opens on the checked option with headings and separators", () => {
    picker();
    expect(button()).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(button());
    expect(button()).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("menu", { name: "Fruit" })).toBeInTheDocument();
    expect(item(/Berry/)).toHaveFocus();
    expect(item(/Berry/)).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText("Pick one")).toBeInTheDocument();
    expect(screen.getByText("Red")).toBeInTheDocument();
    expect(screen.getByText("Dark")).toBeInTheDocument();
    expect(screen.getAllByRole("separator")).toHaveLength(1);
    expect(item(/Cherry/)).toBeDisabled();
  });

  it("moves over the enabled options and picks with a click", () => {
    const onChange = picker();
    fireEvent.keyDown(button(), { key: "ArrowDown" });
    expect(item(/Berry/)).toHaveFocus();
    fireEvent.keyDown(item(/Berry/), { key: "ArrowDown" });
    expect(item(/Date/)).toHaveFocus();
    fireEvent.keyDown(item(/Date/), { key: "ArrowDown" });
    expect(item(/Apple/)).toHaveFocus();
    fireEvent.keyDown(item(/Apple/), { key: "ArrowUp" });
    expect(item(/Date/)).toHaveFocus();
    fireEvent.keyDown(item(/Date/), { key: "Home" });
    expect(item(/Apple/)).toHaveFocus();
    fireEvent.keyDown(item(/Apple/), { key: "End" });
    expect(item(/Date/)).toHaveFocus();
    fireEvent.click(item(/Date/));
    expect(onChange).toHaveBeenCalledWith("d");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(button()).toHaveFocus();
  });

  it("closes on Escape, Tab and a click outside", () => {
    const onChange = picker();
    fireEvent.click(button());
    fireEvent.keyDown(item(/Berry/), { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(button()).toHaveFocus();
    fireEvent.click(button());
    fireEvent.keyDown(item(/Berry/), { key: "Tab" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    fireEvent.click(button());
    fireEvent.mouseDown(screen.getByRole("button", { name: "outside" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    fireEvent.click(button());
    fireEvent.click(button());
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });
});
