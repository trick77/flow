import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { TOOL_KIND_VALUES, type ToolKind } from "../contract/events.gen";
import { formatNumber } from "../i18n/format";
import { Icon, type IconName } from "../shell/Icon";
import type { ActivityRow, FoldItem, GateRow, ToolRow } from "./fold";
import { useFormatDuration, useNow } from "./time";

const kindIcons: Record<ToolKind, IconName> = {
  read: "eye",
  edit: "edit",
  write: "file",
  search: "search",
  bash: "run",
  web: "external",
  todo: "list",
  subagent: "agent",
  mcp: "plug",
  question: "help",
  plan: "map",
  other: "box",
};

// knownKind maps a tool kind this build does not know to other: enum values
// may grow (docs/events.md).
export function knownKind(kind: string): ToolKind {
  return TOOL_KIND_VALUES.includes(kind as ToolKind)
    ? (kind as ToolKind)
    : "other";
}

type Tone = "ok" | "bad" | undefined;

function Summary({ tone, children }: { tone?: Tone; children: ReactNode }) {
  return <span className={tone ? `tsrc ${tone}` : "tsrc"}>{children}</span>;
}

// Lines is "+18 −4"; a zero removal is left out unless always is set.
export function Lines({
  added,
  removed,
  always = false,
}: {
  added?: number;
  removed?: number;
  always?: boolean;
}) {
  const { t } = useTranslation("timeline");
  const showRemoved = removed !== undefined && (always || removed > 0);
  return (
    <>
      {added !== undefined && (
        <span className="add">
          {t("linesAdded", { n: formatNumber(added) })}
        </span>
      )}
      {added !== undefined && showRemoved && " "}
      {showRemoved && (
        <span className="del">
          {t("linesRemoved", { n: formatNumber(removed) })}
        </span>
      )}
    </>
  );
}

// ToolSummary is the right-hand fact of a finished tool row: results, tests,
// +/− lines, a failure.
function ToolSummary({ row }: { row: ToolRow }) {
  const { t } = useTranslation("timeline");
  const fin = row.finished;
  if (!fin) return null;
  const r = fin.result ?? {};
  if (fin.status === "denied") {
    return <Summary tone="bad">{t("toolDenied")}</Summary>;
  }
  if (fin.status === "interrupted") {
    return <Summary>{t("toolInterrupted")}</Summary>;
  }
  if (r.testsFailed !== undefined && r.testsFailed > 0) {
    const total = r.testsFailed + (r.testsPassed ?? 0);
    return (
      <Summary tone="bad">
        {t("testsFailed", {
          failed: formatNumber(r.testsFailed),
          total: formatNumber(total),
        })}
      </Summary>
    );
  }
  if (r.testsPassed !== undefined) {
    return (
      <Summary tone={fin.status === "error" ? "bad" : "ok"}>
        {t("testsPassed", { n: formatNumber(r.testsPassed) })}
      </Summary>
    );
  }
  if (r.matches !== undefined) {
    return (
      <Summary>
        {t("results", { count: r.matches, n: formatNumber(r.matches) })}
      </Summary>
    );
  }
  if (r.added !== undefined || r.removed !== undefined) {
    return (
      <Summary>
        <Lines added={r.added} removed={r.removed} />
      </Summary>
    );
  }
  if (r.pullRequest) {
    return (
      <Summary>
        {t("pullRequest", { n: formatNumber(r.pullRequest.number) })}
      </Summary>
    );
  }
  if (r.exitCode !== undefined && r.exitCode !== 0) {
    return (
      <Summary tone="bad">
        {t("exitCode", { code: formatNumber(r.exitCode) })}
      </Summary>
    );
  }
  if (fin.status === "error") {
    return <Summary tone="bad">{t("toolFailed")}</Summary>;
  }
  return null;
}

// ToolTarget is what the tool works on: the path, command, pattern, URL, or
// for agents and tools without one, their name.
export function ToolTarget({ row }: { row: ToolRow }) {
  const { t } = useTranslation("timeline");
  const kind = knownKind(row.kind);
  switch (kind) {
    case "search":
      return (
        <>
          <span className="mono">{row.pattern ?? row.path ?? row.name}</span>
          {row.pattern !== undefined && row.path !== undefined && (
            <>
              {" "}
              {t("searchIn")} <span className="mono">{row.path}</span>
            </>
          )}
        </>
      );
    case "bash":
      return (
        <span className="mono">
          {row.command ?? row.description ?? row.name}
        </span>
      );
    case "web":
      return <span className="mono">{row.url ?? row.pattern ?? row.name}</span>;
    case "subagent":
      return <span>{row.description ?? row.name}</span>;
    case "todo":
      return null;
    case "mcp":
    case "question":
    case "plan":
    case "other":
      return (
        <span className="mono">{row.path ?? row.command ?? row.name}</span>
      );
    default:
      return <span className="mono">{row.path ?? row.name}</span>;
  }
}

// toolVerb is the row's verb: running or done, Created for a new file.
export function useToolVerb(): (row: ToolRow, running: boolean) => string {
  const { t } = useTranslation("timeline");
  return (row, running) => {
    const kind = knownKind(row.kind);
    if (running) return t(`verbsRunning.${kind}`);
    if (kind === "write" && row.finished?.result?.created) return t("created");
    return t(`verbs.${kind}`);
  };
}

function TraceIcon({ spin, name }: { spin: boolean; name: IconName }) {
  return (
    <span className="ticon">
      {spin ? <span className="spin" /> : <Icon name={name} />}
    </span>
  );
}

function ToolRowView({ row, live }: { row: ToolRow; live: boolean }) {
  const verb = useToolVerb();
  const running = live && !row.finished;
  return (
    <div className={row.nested ? "trow nested" : "trow"} data-testid={row.key}>
      <TraceIcon spin={running} name={kindIcons[knownKind(row.kind)]} />
      <span className="ttext">
        <span className="verb">{verb(row, running)}</span>
        <ToolTarget row={row} />
      </span>
      <ToolSummary row={row} />
    </div>
  );
}

function GateRowView({ row, live }: { row: GateRow; live: boolean }) {
  const { t } = useTranslation("timeline");
  const duration = useFormatDuration();
  const running = live && row.state === "started";
  const repo = row.repo ? <span className="mono">{row.repo}</span> : null;
  if (row.state === "queued") {
    return (
      <div className="trow" data-testid={row.key}>
        <TraceIcon spin={false} name="clock" />
        <span className="ttext">
          <span className="verb">{t("gateWaiting")}</span>
          {repo}
        </span>
        {row.position !== undefined && (
          <Summary>
            {row.aheadRepo
              ? t("gatePositionAfter", {
                  n: formatNumber(row.position),
                  repo: row.aheadRepo,
                })
              : t("gatePosition", { n: formatNumber(row.position) })}
          </Summary>
        )}
      </div>
    );
  }
  const done = row.state === "finished";
  const failed = done && row.exitCode !== 0;
  return (
    <div className="trow" data-testid={row.key}>
      <TraceIcon spin={running} name="shield" />
      <span className="ttext">
        <span className="verb">{t(done ? "gateRan" : "gateRunning")}</span>
        {repo}
      </span>
      {done && (
        <Summary tone={failed ? "bad" : "ok"}>
          {t(failed ? "gateFailed" : "gatePassed")}
          {row.durationMs !== undefined &&
            t("separator") + duration(row.durationMs)}
        </Summary>
      )}
    </div>
  );
}

function Row({ row, live }: { row: ActivityRow; live: boolean }) {
  return row.type === "gate" ? (
    <GateRowView row={row} live={live} />
  ) : (
    <ToolRowView row={row} live={live} />
  );
}

// FoldLabel is "Worked for 2m 14s", or while the turn runs "Working for ...",
// counting from turn.started.
function FoldLabel({ fold }: { fold: FoldItem }) {
  const { t } = useTranslation("timeline");
  const duration = useFormatDuration();
  const now = useNow(fold.running);
  if (fold.running) {
    const start = fold.startedAt ? Date.parse(fold.startedAt) : now;
    return <span>{t("working", { duration: duration(now - start) })}</span>;
  }
  if (fold.durationMs === undefined) {
    return <span>{t("activity")}</span>;
  }
  const label = t("worked", { duration: duration(fold.durationMs) });
  const outcome =
    fold.outcome && fold.outcome !== "completed"
      ? t(`outcome.${fold.outcome}`)
      : null;
  return <span>{outcome ? label + t("separator") + outcome : label}</span>;
}

// ActivityFold is a turn's activity, collapsed once the turn is over unless
// the owner opened it.
export function ActivityFold({ fold }: { fold: FoldItem }) {
  const [toggled, setToggled] = useState<boolean | null>(null);
  const open = toggled ?? fold.running;
  const hasRows = fold.rows.length > 0;
  return (
    <div className="fold">
      <button
        className="fold-btn"
        type="button"
        aria-expanded={hasRows ? open : undefined}
        disabled={!hasRows}
        onClick={() => setToggled(!open)}
      >
        <FoldLabel fold={fold} />
        {hasRows && <span className="chev" />}
      </button>
      {open && hasRows && (
        <div className="fold-body">
          {fold.rows.map((row) => (
            <Row key={row.key} row={row} live={fold.running} />
          ))}
        </div>
      )}
    </div>
  );
}
