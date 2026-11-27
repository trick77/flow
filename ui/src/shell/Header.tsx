import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Icon } from "./Icon";

// Header: the crumb "project / title", the branch chip and, at the right, the
// session toggles (actions).
export function Header({
  project,
  title,
  branch,
  actions,
}: {
  project?: string;
  title: string;
  branch: string;
  actions?: ReactNode;
}) {
  const { t } = useTranslation("shell");
  return (
    <header className="hdr">
      <div className="crumb">
        {project && (
          <>
            <span className="proj">{project}</span>
            <span className="sep" aria-hidden="true">
              {t("crumbSeparator")}
            </span>
          </>
        )}
        <h2 className="ttl">{title}</h2>
      </div>
      {branch !== "" && (
        <span className="chip" title={t("branch")}>
          <Icon name="branch" />
          <span className="mono">{branch}</span>
        </span>
      )}
      {actions}
    </header>
  );
}
