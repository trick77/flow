import { useTranslation } from "react-i18next";

import { Icon } from "./Icon";
import "./shell.css";

// View is what the centre column shows: a session, the start of a new one,
// or one of the two pages.
export type View = "session" | "new" | "projects" | "settings";

// Nav is the header's right end: a divider, then Projects and Settings. The
// button of the page shown is marked current; pressing it again goes back.
export function Nav({
  view,
  onNavigate,
}: {
  view: View;
  onNavigate: (view: View) => void;
}) {
  const { t } = useTranslation("shell");
  const button = (to: "projects" | "settings", icon: "folder" | "sliders") => (
    <button
      type="button"
      className="icon-btn"
      aria-label={t(to)}
      title={t(to)}
      aria-current={view === to ? "page" : undefined}
      onClick={() => onNavigate(view === to ? "session" : to)}
    >
      <Icon name={icon} />
    </button>
  );
  return (
    <div className="hdr-actions">
      <span className="hdr-div" aria-hidden="true" />
      {button("projects", "folder")}
      {button("settings", "sliders")}
    </div>
  );
}
