import { useTranslation } from "react-i18next";

import { Icon } from "./Icon";

// Header: the crumb "project / title" and the branch chip.
export function Header({
  project,
  title,
  branch,
}: {
  project?: string;
  title: string;
  branch: string;
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
    </header>
  );
}
