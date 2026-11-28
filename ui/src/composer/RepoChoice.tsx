import { useTranslation } from "react-i18next";

import type { ProjectRepo } from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { Icon } from "../shell/Icon";
import { MultiPicker } from "./Picker";

// Up to this many repositories show as chips; more get a menu with search.
export const CHIP_MAX = 6;

// RepoChoice is which of the project's repositories a new session works
// in: with one there is nothing to choose and it is named; up to CHIP_MAX
// they are chips to toggle; more are a compact "n of m" button that opens a
// searchable menu.
export function RepoChoice({
  repos,
  picked,
  disabled,
  onToggle,
  onChange,
}: {
  repos: readonly ProjectRepo[];
  // The ids of the repositories in the session.
  picked: readonly string[];
  disabled: boolean;
  onToggle: (id: string) => void;
  onChange: (ids: string[]) => void;
}) {
  const { t } = useTranslation("composer");
  if (repos.length === 0) return null;
  if (repos.length === 1) {
    return (
      <p className="rpick-one">{t("workingIn", { repo: repos[0].fullName })}</p>
    );
  }
  if (repos.length <= CHIP_MAX) {
    return (
      <div className="rpick" role="group" aria-label={t("repos")}>
        <span className="rpick-label" aria-hidden="true">
          {t("repos")}
        </span>
        <div className="rpick-chips">
          {repos.map((r) => (
            <label className="rchip" key={r.id} title={r.fullName}>
              <input
                type="checkbox"
                checked={picked.includes(r.id)}
                disabled={disabled}
                onChange={() => onToggle(r.id)}
              />
              <span className="box" aria-hidden="true">
                <Icon name="check" />
              </span>
              <span className="nm">{r.fullName}</span>
            </label>
          ))}
        </div>
      </div>
    );
  }
  const count = t("repoPicked", {
    count: repos.length,
    picked: formatNumber(picked.length),
    n: formatNumber(repos.length),
  });
  return (
    <div className="rpick rpick-many">
      <MultiPicker
        label={`${t("repos")}: ${count}`}
        values={picked}
        options={repos.map((r) => ({ value: r.id, label: r.fullName }))}
        onChange={onChange}
        search={{
          label: t("repoSearch"),
          placeholder: t("repoSearchPlaceholder"),
          empty: t("repoSearchNone"),
        }}
        all={t("repoAll")}
        none={t("repoNone")}
        disabled={disabled}
        className="rchip rpick-btn"
      >
        <Icon name="stack" />
        <span>{count}</span>
      </MultiPicker>
    </div>
  );
}
