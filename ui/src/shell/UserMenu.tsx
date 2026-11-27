import { useContext, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { AccountContext } from "./AuthGate";
import { Icon } from "./Icon";

// UserMenu is the sidebar's foot: the owner's name and a menu with sign-out.
// Renders nothing outside AuthGate.
export function UserMenu() {
  const account = useContext(AccountContext);
  const { t } = useTranslation("shell");
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);

  // Escape or a click elsewhere closes the menu.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [open]);

  if (!account) return null;
  return (
    <div
      className="relative flex-none border-t border-border-side px-2.5 pt-2 pb-2.5"
      ref={wrap}
    >
      <button
        className="flex w-full items-center gap-2.5 rounded-lg p-1.5 transition-colors hover:bg-active aria-expanded:bg-active"
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("account", { name: account.name })}
        onClick={() => setOpen((o) => !o)}
      >
        <span
          className="grid size-8 flex-none place-items-center rounded-full bg-ink-dim text-[0.8125rem] font-semibold text-bg"
          aria-hidden="true"
        >
          {account.name.slice(0, 1).toUpperCase()}
        </span>
        <span className="min-w-0 flex-1 text-left">
          <span className="block truncate text-ink-bright">{account.name}</span>
          <span className="block truncate text-xs text-faint">
            {t("owner")}
          </span>
        </span>
      </button>
      {open && (
        <div
          className="absolute right-2.5 bottom-[calc(100%+6px)] left-2.5 z-40 rounded-ui border border-border-strong bg-popover py-1 shadow-lg"
          role="menu"
        >
          <button
            className="mx-1 flex w-[calc(100%-8px)] items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-ink-bright hover:bg-active"
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              account.signOut();
            }}
          >
            <Icon name="signout" />
            <span>{t("signOut")}</span>
          </button>
        </div>
      )}
    </div>
  );
}
