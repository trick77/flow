import { createContext, useContext } from "react";
import { useTranslation } from "react-i18next";

import { Icon } from "./Icon";

// Drawer is the sidebar as an off-canvas drawer on narrow screens.
export interface Drawer {
  narrow: boolean;
  open: boolean;
  // openDrawer opens it; opener gets focus back when it closes.
  openDrawer: (opener?: HTMLElement) => void;
}

// DrawerContext is provided by the app; without it (a page rendered alone)
// there is no drawer.
export const DrawerContext = createContext<Drawer | null>(null);

// SIDEBAR_ID names the sidebar for the menu button's aria-controls.
export const SIDEBAR_ID = "sidebar";

// MenuButton opens the sidebar drawer; it is at the left of every header and
// only there on narrow screens.
export function MenuButton() {
  const { t } = useTranslation("shell");
  const drawer = useContext(DrawerContext);
  if (!drawer?.narrow) return null;
  return (
    <button
      type="button"
      className="icon-btn menu-btn"
      aria-label={t("openSidebar")}
      aria-controls={SIDEBAR_ID}
      aria-expanded={drawer.open}
      onClick={(e) => drawer.openDrawer(e.currentTarget)}
    >
      <Icon name="menu" />
    </button>
  );
}
