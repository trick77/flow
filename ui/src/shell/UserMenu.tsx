import {
  useContext,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { AccountContext } from "./AuthGate";
import { Icon } from "./Icon";

// Avatar is the owner's initial in a disc.
function initial(name: string): string {
  return [...name.trim()].slice(0, 1).join("").toLocaleUpperCase("en-CH");
}

// UserMenu is the sidebar's foot: the owner's avatar, name, role and the
// host Flow runs on, and a menu with Settings and sign-out. In the rail
// (compact) only the avatar shows; it opens Settings. Renders nothing
// outside AuthGate.
export function UserMenu({
  onOpenSettings,
  compact = false,
}: {
  onOpenSettings?: () => void;
  compact?: boolean;
}) {
  const account = useContext(AccountContext);
  const { t } = useTranslation("shell");
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);

  // A click elsewhere closes the menu; opening it focuses the first item.
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  if (!account) return null;
  const name = account.name;
  const host = window.location.host;

  if (compact) {
    return (
      <div className="rail-col rail-foot">
        <button
          type="button"
          className="avatar"
          aria-label={t("account", { name })}
          title={t("settings")}
          onClick={onOpenSettings}
        >
          {initial(name)}
        </button>
      </div>
    );
  }

  // Escape closes only the menu (not a drawer around it); Up and Down move.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      button.current?.focus();
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = [
      ...(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ??
        []),
    ];
    if (items.length === 0) return;
    e.preventDefault();
    const i = items.indexOf(document.activeElement as HTMLElement);
    const step = e.key === "ArrowDown" ? 1 : -1;
    items[(i + step + items.length) % items.length].focus();
  };

  return (
    <div className="sb-foot user-wrap" ref={wrap} onKeyDown={onKeyDown}>
      <button
        ref={button}
        className="user-btn"
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("account", { name })}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="avatar" aria-hidden="true">
          {initial(name)}
        </span>
        <span className="user-meta">
          <span className="nm">{name}</span>
          <span className="sub">
            {host ? t("ownerAt", { host }) : t("owner")}
          </span>
        </span>
        <Icon name="chevDown" />
      </button>
      {open && (
        <div className="pop user-pop" role="menu" ref={menu}>
          {onOpenSettings && (
            <button
              className="opt"
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                onOpenSettings();
              }}
            >
              <Icon name="sliders" />
              <span className="grow">{t("settings")}</span>
            </button>
          )}
          <button
            className="opt"
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              account.signOut();
            }}
          >
            <Icon name="signout" />
            <span className="grow">{t("signOut")}</span>
          </button>
        </div>
      )}
    </div>
  );
}
