import { useState, type KeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Icon } from "./Icon";

// Title is the session's title; with onRename a click turns it into a field:
// Enter or leaving it saves, Escape cancels. An empty or unchanged title
// saves nothing.
function Title({
  title,
  onRename,
}: {
  title: string;
  onRename?: (title: string) => void;
}) {
  const { t } = useTranslation("shell");
  const [draft, setDraft] = useState<string | null>(null);
  if (!onRename) return <h2 className="ttl">{title}</h2>;

  const done = (save: boolean) => {
    const next = draft?.trim() ?? "";
    setDraft(null);
    if (save && next !== "" && next !== title) onRename(next);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && !e.nativeEvent.isComposing) {
      e.preventDefault();
      done(true);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      done(false);
    }
  };

  if (draft !== null) {
    return (
      <input
        className="ttl ttl-edit"
        aria-label={t("renameLabel")}
        value={draft}
        autoFocus
        onFocus={(e) => e.currentTarget.select()}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        onBlur={() => done(true)}
      />
    );
  }
  return (
    <h2 className="ttl">
      <button
        type="button"
        className="ttl-btn"
        title={t("rename")}
        aria-description={t("rename")}
        onClick={() => setDraft(title)}
      >
        {title}
      </button>
    </h2>
  );
}

// Header: the crumb "project / title", the branch chip and, at the right, the
// session toggles (actions). With onRename the title can be edited in place.
export function Header({
  project,
  title,
  branch,
  actions,
  onRename,
}: {
  project?: string;
  title: string;
  branch: string;
  actions?: ReactNode;
  onRename?: (title: string) => void;
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
        <Title title={title} onRename={onRename} />
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
