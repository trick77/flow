import { useTranslation } from "react-i18next";

import type {
  ErrorCode,
  Project,
  SessionStatus,
  SessionSummary,
} from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { Icon } from "../shell/Icon";
import { groupSessions, SECTION_ORDER, type SectionId } from "./group";

const sectionKeys = {
  needs: "sectionNeeds",
  working: "sectionWorking",
  done: "sectionDone",
  idle: "sectionIdle",
} as const satisfies Record<SectionId, string>;

const statusKeys = {
  approval: "statusApproval",
  question: "statusQuestion",
  plan: "statusPlan",
  working: "statusWorking",
  queued: "statusQueued",
  done: "statusDone",
  failed: "statusFailed",
  idle: "statusIdle",
} as const satisfies Record<SessionStatus, string>;

// statusClass picks the dot color; plan waits on the owner like a question.
function statusClass(status: SessionStatus): string {
  if (status === "plan") return "st-question";
  if (status === "queued") return "st-working";
  return status in statusKeys ? `st-${status}` : "st-idle";
}

function SessionRow({
  session,
  project,
  selected,
  onSelect,
}: {
  session: SessionSummary;
  project?: Project;
  selected: boolean;
  onSelect: (id: string) => void;
}) {
  const { t } = useTranslation("sidebar");
  const name = project?.name ?? "";
  const statusKey =
    (statusKeys as Record<string, (typeof statusKeys)[SessionStatus]>)[
      session.status
    ] ?? "statusIdle";
  return (
    <button
      className="srow"
      type="button"
      aria-current={selected ? "true" : undefined}
      onClick={() => onSelect(session.id)}
    >
      <span className="s-proj">
        <span className="mono-badge" aria-hidden="true">
          {name.slice(0, 1)}
        </span>
        <span>{name}</span>
      </span>
      <span className="s-right">
        <span className={`status ${statusClass(session.status)}`}>
          {t(statusKey)}
        </span>
      </span>
      <span className="s-title">{session.title}</span>
      <span className="s-branch">
        <Icon name="branch" />
        <span>{session.branch}</span>
      </span>
    </button>
  );
}

// Sidebar: the wordmark and the sessions in their status sections.
export function Sidebar({
  sessions,
  projects,
  selectedId,
  onSelect,
  loaded,
  error,
}: {
  sessions: SessionSummary[];
  projects: Project[];
  selectedId?: string;
  onSelect: (id: string) => void;
  loaded: boolean;
  error: ErrorCode | null;
}) {
  const { t } = useTranslation("sidebar");
  const sections = groupSessions(sessions);
  const projectOf = new Map(projects.map((p) => [p.id, p]));
  return (
    <aside className="sidebar" aria-label={t("label")}>
      <div className="sb-top">
        <h1 className="wordmark">{t("appName", { ns: "common" })}</h1>
      </div>
      <nav className="sb-list" aria-label={t("listLabel")}>
        {error && (
          <p className="sb-error" role="alert">
            {t(error, { ns: "errors" })}
          </p>
        )}
        {!loaded && <p className="sb-empty">{t("loading")}</p>}
        {loaded &&
          SECTION_ORDER.map((id) => (
            <section
              className="sb-section"
              key={id}
              aria-label={t(sectionKeys[id])}
            >
              <h2 className="sb-section-h">
                <span>{t(sectionKeys[id])}</span>
                <span className="n">{formatNumber(sections[id].length)}</span>
              </h2>
              {sections[id].length === 0 ? (
                <p className="sb-empty">{t("empty")}</p>
              ) : (
                sections[id].map((s) => (
                  <SessionRow
                    key={s.id}
                    session={s}
                    project={projectOf.get(s.projectId)}
                    selected={s.id === selectedId}
                    onSelect={onSelect}
                  />
                ))
              )}
            </section>
          ))}
      </nav>
    </aside>
  );
}
