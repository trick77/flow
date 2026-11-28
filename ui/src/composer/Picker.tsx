import {
  useEffect,
  useId,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";

import { Icon } from "../shell/Icon";

export interface PickerOption<T extends string> {
  value: T;
  label: string;
  description?: string;
  // Shown before the label (a badge, a pill).
  lead?: ReactNode;
  disabled?: boolean;
  // The option is the risky one (full access).
  warn?: boolean;
  // A heading shown above the first option of its group.
  group?: string;
}

// The search field of a long menu: its accessible name, its placeholder and
// what it says when nothing matches.
export interface PickerSearch {
  label: string;
  placeholder: string;
  empty: string;
}

// usePopup is a menu's open state: a mousedown outside wrap closes it.
function usePopup(): [
  boolean,
  (open: boolean | ((o: boolean) => boolean)) => void,
  RefObject<HTMLDivElement | null>,
] {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  return [open, setOpen, wrap];
}

// The least height a menu is given below its button before it opens above
// instead, in px.
const MIN_BELOW = 220;

// fitPopup is where a menu opened under el fits: the room between el and
// the bottom of the nearest clipping ancestor (the timeline ends at the
// composer) or, when that is short and the room above is larger, above el.
export function fitPopup(el: HTMLElement): {
  placement: "up" | "down";
  maxHeight: number;
} {
  const r = el.getBoundingClientRect();
  let top = 0;
  let bottom = window.innerHeight;
  for (let a = el.parentElement; a; a = a.parentElement) {
    const s = getComputedStyle(a);
    if (/(auto|scroll|hidden|clip)/.test(s.overflowY)) {
      const ar = a.getBoundingClientRect();
      top = Math.max(top, ar.top);
      bottom = Math.min(bottom, ar.bottom);
      break;
    }
  }
  const gap = 14;
  const below = bottom - r.bottom - gap;
  const above = r.top - top - gap;
  return below >= MIN_BELOW || below >= above
    ? { placement: "down", maxHeight: Math.max(below, 0) }
    : { placement: "up", maxHeight: Math.max(above, 0) };
}

// matches: every word of the query occurs in the option's label or
// description, ignoring case.
function matches(o: PickerOption<string>, query: string): boolean {
  const text = `${o.label} ${o.description ?? ""}`.toLocaleLowerCase();
  return query
    .toLocaleLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => text.includes(w));
}

// SearchList is a long menu's body: a search field that keeps the focus and
// the matching options as a listbox, as in the session search. Up and Down
// move the active option, Enter picks it (onPick), Escape closes (onClose).
// The options keep the order they had when it opened: a list that moves
// under the pointer picks the wrong one.
function SearchList<T extends string>({
  label,
  search,
  options,
  checked,
  multi = false,
  onPick,
  onClose,
  head,
}: {
  label: string;
  search: PickerSearch;
  options: readonly PickerOption<T>[];
  checked: (value: T) => boolean;
  multi?: boolean;
  onPick: (value: T) => void;
  onClose: () => void;
  // Shown between the field and the list (select all / none), given the
  // options the query leaves.
  head?: (shown: readonly PickerOption<T>[]) => ReactNode;
}) {
  const id = useId();
  const listId = `${id}-list`;
  const optionId = (i: number) => `${id}-opt-${i}`;
  const input = useRef<HTMLInputElement>(null);
  const [order] = useState(() => options.map((o) => o.value));
  const rank = (v: T) => {
    const at = order.indexOf(v);
    return at < 0 ? order.length : at;
  };
  const [query, setQuery] = useState("");
  const shown = [...options]
    .sort((a, b) => rank(a.value) - rank(b.value))
    .filter((o) => matches(o, query));
  const enabled = (i: number) => !shown[i]?.disabled;
  const [active, setActive] = useState(() =>
    Math.max(
      shown.findIndex((o) => !o.disabled && checked(o.value)),
      0,
    ),
  );
  const current = Math.min(active, Math.max(shown.length - 1, 0));
  const activeId = shown.length > 0 ? optionId(current) : undefined;

  useEffect(() => {
    input.current?.focus();
  }, []);
  useEffect(() => {
    if (activeId)
      document.getElementById(activeId)?.scrollIntoView?.({ block: "nearest" });
  }, [activeId]);

  const move = (step: number) => {
    if (shown.length === 0) return;
    let i = current;
    for (let n = 0; n < shown.length; n++) {
      i = (i + step + shown.length) % shown.length;
      if (enabled(i)) break;
    }
    setActive(i);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp":
        e.preventDefault();
        move(e.key === "ArrowDown" ? 1 : -1);
        return;
      case "Enter": {
        if (e.nativeEvent.isComposing) return;
        e.preventDefault();
        const o = shown[current];
        if (o && !o.disabled) onPick(o.value);
        return;
      }
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        onClose();
        return;
    }
  };

  return (
    <>
      <div className="pop-search">
        <Icon name="search" />
        <input
          ref={input}
          type="text"
          role="combobox"
          aria-label={search.label}
          aria-expanded={shown.length > 0}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={activeId}
          placeholder={search.placeholder}
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
        />
      </div>
      {head?.(shown)}
      {shown.length === 0 && (
        <p className="pop-empty" role="status">
          {search.empty}
        </p>
      )}
      <ul
        className="pop-list"
        role="listbox"
        id={listId}
        aria-label={label}
        aria-multiselectable={multi || undefined}
      >
        {shown.map((o, i) => (
          <li
            key={o.value}
            id={optionId(i)}
            role="option"
            aria-selected={checked(o.value)}
            aria-disabled={o.disabled || undefined}
            title={o.label}
            className={[
              "opt",
              o.warn ? "warn" : "",
              i === current ? "active" : "",
              o.disabled ? "off" : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onMouseMove={() => i !== current && !o.disabled && setActive(i)}
            onClick={() => !o.disabled && onPick(o.value)}
          >
            <span className="ck" aria-hidden="true">
              <Icon name="check" />
            </span>
            {o.lead}
            <span className="grow">
              <span className="t">{o.label}</span>
              {o.description && <span className="d">{o.description}</span>}
            </span>
          </li>
        ))}
      </ul>
    </>
  );
}

// Picker is a button that opens a menu of choices, one of them checked (the
// mock's model and mode pickers). Arrows move, Enter or a click picks,
// Escape closes; focus goes back to the button. With search the menu has a
// search field that keeps the focus (a long list: projects).
export function Picker<T extends string>({
  label,
  heading,
  value,
  options,
  onChange,
  children,
  className = "picker",
  placement = "up",
  note,
  disabled = false,
  title,
  search,
}: {
  // The button's accessible name.
  label: string;
  // A heading at the top of the menu.
  heading?: string;
  // A line under the heading (why some options are disabled).
  note?: string;
  // The button cannot open the menu; title says why.
  disabled?: boolean;
  title?: string;
  value: T | undefined;
  options: readonly PickerOption<T>[];
  onChange: (value: T) => void;
  // The button's content.
  children: ReactNode;
  className?: string;
  placement?: "up" | "down";
  search?: PickerSearch;
}) {
  const [open, setOpen, wrap] = usePopup();
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const menuId = useId();

  const items = () =>
    Array.from(
      menu.current?.querySelectorAll<HTMLButtonElement>(
        '[role="menuitemradio"]:not(:disabled)',
      ) ?? [],
    );

  // On open, focus the checked option (or the first one).
  useEffect(() => {
    if (!open || search) return;
    const all = items();
    (
      all.find((b) => b.getAttribute("aria-checked") === "true") ?? all[0]
    )?.focus();
  }, [open, search]);

  const close = () => {
    setOpen(false);
    button.current?.focus();
  };

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Tab") {
      setOpen(false);
      return;
    }
    if (search) return;
    const all = items();
    const at = all.indexOf(document.activeElement as HTMLButtonElement);
    const go = (i: number) => {
      e.preventDefault();
      all[(i + all.length) % all.length]?.focus();
    };
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        close();
        return;
      case "ArrowDown":
        return go(at + 1);
      case "ArrowUp":
        return go(at - 1);
      case "Home":
        return go(0);
      case "End":
        return go(all.length - 1);
    }
  };

  return (
    <div className="pick-wrap" ref={wrap}>
      <button
        ref={button}
        type="button"
        className={className}
        aria-label={label}
        aria-haspopup={search ? "dialog" : "menu"}
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        disabled={disabled}
        title={title}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        {children}
        <Icon name="chevDown" />
      </button>
      {open && (
        <div
          className={search ? `pop ${placement} pop-find` : `pop ${placement}`}
          role={search ? "dialog" : "menu"}
          id={menuId}
          aria-label={label}
          ref={menu}
          onKeyDown={onMenuKey}
          // A click on a heading, a separator, a disabled option or the
          // padding would move focus to the body and leave the menu deaf to
          // its keys: focus stays where it is (an option's click still picks,
          // the search field still takes a click).
          onMouseDown={(e) => {
            if (!(e.target instanceof HTMLInputElement)) e.preventDefault();
          }}
        >
          {heading && !search && (
            <div className="pop-h" aria-hidden="true">
              {heading}
            </div>
          )}
          {note && <div className="pop-h pop-note">{note}</div>}
          {search ? (
            <SearchList
              label={label}
              search={search}
              options={options}
              checked={(v) => v === value}
              onPick={(v) => {
                onChange(v);
                close();
              }}
              onClose={close}
            />
          ) : (
            options.map((o, i) => {
              const head =
                o.group !== undefined &&
                (i === 0 || options[i - 1].group !== o.group);
              return (
                <div key={o.value} role="none">
                  {head && (
                    <>
                      {i > 0 && <div className="pop-sep" role="separator" />}
                      <div className="pop-h" role="presentation">
                        {o.group}
                      </div>
                    </>
                  )}
                  <button
                    type="button"
                    role="menuitemradio"
                    aria-checked={o.value === value}
                    className={o.warn ? "opt warn" : "opt"}
                    disabled={o.disabled}
                    onClick={() => {
                      onChange(o.value);
                      close();
                    }}
                  >
                    <span className="ck" aria-hidden="true">
                      <Icon name="check" />
                    </span>
                    {o.lead}
                    <span className="grow">
                      <span className="t">{o.label}</span>
                      {o.description && (
                        <span className="d">{o.description}</span>
                      )}
                    </span>
                  </button>
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}

// MultiPicker is a Picker with several choices checked: a search field, the
// options with check marks, select all and none. Enter or a click toggles an
// option and the menu stays open; Escape, a click outside or Tab past its
// last control closes it.
export function MultiPicker<T extends string>({
  label,
  values,
  options,
  onChange,
  children,
  search,
  all,
  none,
  disabled = false,
  className = "picker",
}: {
  label: string;
  values: readonly T[];
  options: readonly PickerOption<T>[];
  onChange: (values: T[]) => void;
  children: ReactNode;
  search: PickerSearch;
  // The select all and select none buttons' text.
  all: string;
  none: string;
  disabled?: boolean;
  className?: string;
}) {
  const [open, setOpen, wrap] = usePopup();
  const button = useRef<HTMLButtonElement>(null);
  const menuId = useId();
  // Where the menu fits, measured as it opens: below the button down to
  // where the scrolling area ends, or above it when that room is short.
  const [fit, setFit] = useState<ReturnType<typeof fitPopup>>({
    placement: "down",
    maxHeight: 0,
  });
  const show = () => {
    if (button.current) setFit(fitPopup(button.current));
    setOpen(true);
  };
  const close = () => {
    setOpen(false);
    button.current?.focus();
  };
  // inOrder: the values in the options' order.
  const inOrder = (vs: readonly T[]) =>
    options.map((o) => o.value).filter((v) => vs.includes(v));
  const toggle = (v: T) =>
    onChange(
      values.includes(v)
        ? values.filter((x) => x !== v)
        : inOrder([...values, v]),
    );
  // Focus that moves to another element (Tab past None) closes it. A blur
  // to nowhere does not: Safari focuses no button on a click, and a switch
  // to another window is no choice; a click outside closes it (usePopup).
  const onBlur = (e: FocusEvent<HTMLDivElement>) => {
    const to = e.relatedTarget as Node | null;
    if (to && !wrap.current?.contains(to)) setOpen(false);
  };
  return (
    <div className="pick-wrap" ref={wrap}>
      <button
        ref={button}
        type="button"
        className={className}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            show();
          }
        }}
      >
        {children}
        <Icon name="chevDown" />
      </button>
      {open && (
        <div
          className={`pop ${fit.placement} pop-find`}
          style={fit.maxHeight > 0 ? { maxHeight: fit.maxHeight } : undefined}
          role="dialog"
          id={menuId}
          aria-label={label}
          onBlur={onBlur}
          onKeyDown={(e) => {
            if (e.key !== "Escape") return;
            e.preventDefault();
            e.stopPropagation();
            close();
          }}
          // Focus stays in the search field on a click anywhere in the menu,
          // select all and none included (their click still acts), the
          // same in every browser.
          onMouseDown={(e) => {
            if (!(e.target instanceof HTMLInputElement)) e.preventDefault();
          }}
        >
          <SearchList
            label={label}
            search={search}
            options={options}
            checked={(v) => values.includes(v)}
            multi
            onPick={toggle}
            onClose={close}
            head={(shown) => {
              // All and none act on the options the query leaves.
              const these = shown
                .filter((o) => !o.disabled)
                .map((o) => o.value);
              return (
                <div className="pop-acts">
                  {/* aria-disabled, not disabled: a focused button that
                      turns disabled drops the focus, and the menu with it. */}
                  <button
                    type="button"
                    className="pop-act"
                    aria-disabled={these.every((v) => values.includes(v))}
                    onClick={() => onChange(inOrder([...values, ...these]))}
                  >
                    {all}
                  </button>
                  <button
                    type="button"
                    className="pop-act"
                    aria-disabled={!these.some((v) => values.includes(v))}
                    onClick={() =>
                      onChange(values.filter((v) => !these.includes(v)))
                    }
                  >
                    {none}
                  </button>
                </div>
              );
            }}
          />
        </div>
      )}
    </div>
  );
}
