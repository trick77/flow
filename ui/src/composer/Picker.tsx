import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
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

// Picker is a button that opens a menu of choices, one of them checked (the
// mock's model and mode pickers). Arrows move, Enter or a click picks,
// Escape closes; focus goes back to the button.
export function Picker<T extends string>({
  label,
  heading,
  value,
  options,
  onChange,
  children,
  className = "picker",
  placement = "up",
}: {
  // The button's accessible name.
  label: string;
  // A heading at the top of the menu.
  heading?: string;
  value: T | undefined;
  options: readonly PickerOption<T>[];
  onChange: (value: T) => void;
  // The button's content.
  children: ReactNode;
  className?: string;
  placement?: "up" | "down";
}) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
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
    if (!open) return;
    const all = items();
    (
      all.find((b) => b.getAttribute("aria-checked") === "true") ?? all[0]
    )?.focus();
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const close = () => {
    setOpen(false);
    button.current?.focus();
  };

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
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
      case "Tab":
        setOpen(false);
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
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
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
          className={`pop ${placement}`}
          role="menu"
          id={menuId}
          aria-label={label}
          ref={menu}
          onKeyDown={onMenuKey}
        >
          {heading && (
            <div className="pop-h" aria-hidden="true">
              {heading}
            </div>
          )}
          {options.map((o, i) => {
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
          })}
        </div>
      )}
    </div>
  );
}
