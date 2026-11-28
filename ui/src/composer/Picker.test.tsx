import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { fitPopup, MultiPicker, Picker } from "./Picker";

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

  it("keeps focus in the menu on a click beside the options", () => {
    picker();
    fireEvent.click(button());
    // fireEvent returns false when the default (moving focus) is prevented.
    expect(fireEvent.mouseDown(screen.getByText("Red"))).toBe(false);
    expect(fireEvent.mouseDown(screen.getByRole("menu"))).toBe(false);
    expect(item(/Berry/)).toHaveFocus();
    fireEvent.keyDown(item(/Berry/), { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("searches with the focus in the field, skipping disabled options", () => {
    const onChange = vi.fn();
    render(
      <Picker
        label="Fruit"
        heading="Pick one"
        value="a"
        onChange={onChange}
        search={{ label: "Find", placeholder: "Find", empty: "None" }}
        options={[
          { value: "a", label: "Apple" },
          { value: "c", label: "Cherry", disabled: true },
          { value: "d", label: "Date", description: "dried" },
        ]}
      >
        Apple
      </Picker>,
    );
    fireEvent.click(button());
    const field = screen.getByRole("combobox", { name: "Find" });
    expect(field).toHaveFocus();
    // The heading gives way to the field.
    expect(screen.queryByText("Pick one")).not.toBeInTheDocument();
    const opt = (name: RegExp) => screen.getByRole("option", { name });
    expect(opt(/Cherry/)).toHaveAttribute("aria-disabled", "true");
    fireEvent.keyDown(field, { key: "ArrowDown" });
    expect(field).toHaveAttribute("aria-activedescendant", opt(/Date/).id);
    fireEvent.keyDown(field, { key: "ArrowDown" });
    expect(field).toHaveAttribute("aria-activedescendant", opt(/Apple/).id);
    fireEvent.keyDown(field, { key: "ArrowUp" });
    expect(field).toHaveAttribute("aria-activedescendant", opt(/Date/).id);
    // A click on a disabled one does nothing; the mouse moves the active.
    fireEvent.click(opt(/Cherry/));
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.mouseMove(opt(/Apple/));
    expect(field).toHaveAttribute("aria-activedescendant", opt(/Apple/).id);
    // The field takes a click; the list keeps the focus where it is.
    expect(fireEvent.mouseDown(field)).toBe(true);
    expect(fireEvent.mouseDown(opt(/Apple/))).toBe(false);
    // The description is searched too.
    fireEvent.change(field, { target: { value: "dried" } });
    expect(screen.getAllByRole("option")).toHaveLength(1);
    fireEvent.change(field, { target: { value: "zzz" } });
    expect(screen.getByRole("status")).toHaveTextContent("None");
    fireEvent.keyDown(field, { key: "ArrowDown" });
    fireEvent.keyDown(field, { key: "Enter" });
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.change(field, { target: { value: "" } });
    fireEvent.click(opt(/Date/));
    expect(onChange).toHaveBeenCalledWith("d");
    expect(button()).toHaveFocus();
    fireEvent.click(button());
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Tab" });
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("keeps the order it opened with while options move", () => {
    const opts = [
      { value: "a", label: "Apple" },
      { value: "b", label: "Berry" },
    ];
    const search = { label: "Find", placeholder: "Find", empty: "None" };
    const { rerender } = render(
      <Picker
        label="Fruit"
        value="a"
        onChange={vi.fn()}
        search={search}
        options={opts}
      >
        A
      </Picker>,
    );
    fireEvent.click(button());
    rerender(
      <Picker
        label="Fruit"
        value="a"
        onChange={vi.fn()}
        search={search}
        options={[...opts].reverse().concat({ value: "c", label: "Cherry" })}
      >
        A
      </Picker>,
    );
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Apple",
      "Berry",
      "Cherry",
    ]);
  });

  it("closes a multi picker when the focus leaves it", () => {
    const onChange = vi.fn();
    render(
      <>
        <MultiPicker
          label="Repos"
          values={["a"]}
          onChange={onChange}
          search={{ label: "Find", placeholder: "Find", empty: "None" }}
          all="All"
          none="None"
          options={[
            { value: "a", label: "Apple" },
            { value: "b", label: "Berry" },
          ]}
        >
          1 of 2
        </MultiPicker>
        <button type="button">outside</button>
      </>,
    );
    const open = screen.getByRole("button", { name: "Repos" });
    fireEvent.keyDown(open, { key: "ArrowUp" });
    const none = screen.getByRole("button", { name: "None" });
    // Inside: stays open.
    fireEvent.blur(screen.getByRole("combobox"), { relatedTarget: none });
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    fireEvent.keyDown(none, { key: "Escape" });
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(open).toHaveFocus();
    fireEvent.click(open);
    // A click anywhere in the menu, All and None included, keeps the focus
    // in the field; only the field itself takes a click.
    expect(fireEvent.mouseDown(screen.getByRole("dialog"))).toBe(false);
    expect(
      fireEvent.mouseDown(screen.getByRole("button", { name: "All" })),
    ).toBe(false);
    expect(fireEvent.mouseDown(screen.getByRole("combobox"))).toBe(true);
    // Safari: a click on a button focuses nothing, the blur goes nowhere
    // (relatedTarget null) before the click. The menu stays and the click
    // acts.
    fireEvent.blur(screen.getByRole("combobox"), { relatedTarget: null });
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    expect(onChange).toHaveBeenLastCalledWith(["a", "b"]);
    fireEvent.blur(screen.getByRole("combobox"), { relatedTarget: null });
    fireEvent.click(screen.getByRole("button", { name: "None" }));
    expect(onChange).toHaveBeenLastCalledWith([]);
    // The same on the trigger: the click closes the menu, not the blur.
    fireEvent.blur(screen.getByRole("combobox"), { relatedTarget: null });
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    fireEvent.click(open);
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    fireEvent.click(open);
    fireEvent.click(screen.getByRole("option", { name: "Berry" }));
    expect(onChange).toHaveBeenLastCalledWith(["a", "b"]);
    fireEvent.click(screen.getByRole("option", { name: "Apple" }));
    expect(onChange).toHaveBeenLastCalledWith([]);
    fireEvent.blur(screen.getByRole("combobox"), {
      relatedTarget: screen.getByRole("button", { name: "outside" }),
    });
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("shows a note and can be disabled", () => {
    render(
      <Picker
        label="Mode"
        note="Capped"
        value="a"
        options={[{ value: "a", label: "A" }]}
        onChange={vi.fn()}
        disabled
        title="Not now"
      >
        A
      </Picker>,
    );
    const b = screen.getByRole("button", { name: "Mode" });
    expect(b).toBeDisabled();
    expect(b).toHaveAttribute("title", "Not now");
    cleanup();
    render(
      <Picker
        label="Mode"
        note="Capped"
        value="a"
        options={[{ value: "a", label: "A" }]}
        onChange={vi.fn()}
      >
        A
      </Picker>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Mode" }));
    expect(screen.getByText("Capped")).toBeInTheDocument();
  });
});

describe("fitPopup", () => {
  const rect = (top: number, bottom: number) =>
    ({
      top,
      bottom,
      left: 0,
      right: 0,
      width: 0,
      height: bottom - top,
    }) as DOMRect;

  function place(buttonTop: number, buttonBottom: number) {
    const scroller = document.createElement("div");
    scroller.style.overflowY = "auto";
    const el = document.createElement("button");
    scroller.append(el);
    document.body.append(scroller);
    scroller.getBoundingClientRect = () => rect(50, 650);
    el.getBoundingClientRect = () => rect(buttonTop, buttonBottom);
    const got = fitPopup(el);
    scroller.remove();
    return got;
  }

  it("opens below down to where the scrolling area ends", () => {
    expect(place(100, 130)).toEqual({ placement: "down", maxHeight: 506 });
  });

  it("opens above when the room below is short and above is larger", () => {
    expect(place(500, 530)).toEqual({ placement: "up", maxHeight: 436 });
  });

  it("stays below when above is no better", () => {
    expect(place(200, 500)).toEqual({ placement: "down", maxHeight: 136 });
  });
});
