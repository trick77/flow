import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, getSetupLog } from "../api";
import type { ErrorCode, SetupLine } from "../api/types.gen";

// The API answers at most this many lines at once; a full page means more.
const PAGE = 1000;
// Pages read at most (a runaway log stops here; each log keeps its first
// 4 MiB anyway).
const MAX_PAGES = 20;

type State =
  | { kind: "loading" }
  | { kind: "error"; code: ErrorCode }
  | { kind: "ready"; lines: SetupLine[] };

// SetupLog shows the last setup run's log of a project's sandbox, all its
// pages read in turn, in a scrollable monospace block.
export function SetupLog({ projectId }: { projectId: string }) {
  const { t } = useTranslation("projects");
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    let live = true;
    const read = async () => {
      const lines: SetupLine[] = [];
      for (let page = 0; page < MAX_PAGES; page++) {
        const after = lines.length > 0 ? lines[lines.length - 1].n : 0;
        const got = await getSetupLog(projectId, after);
        lines.push(...got.lines);
        if (got.lines.length < PAGE) break;
      }
      return lines;
    };
    read().then(
      (lines) => {
        if (live) setState({ kind: "ready", lines });
      },
      (err: unknown) => {
        if (live) setState({ kind: "error", code: errorCode(err) });
      },
    );
    return () => {
      live = false;
    };
  }, [projectId]);

  if (state.kind === "loading") {
    return (
      <p className="setup-log-note" role="status">
        {t("setupLogLoading")}
      </p>
    );
  }
  if (state.kind === "error") {
    return (
      <p className="pcard-err" role="alert">
        {t("setupLogFailed")} {t(state.code, { ns: "errors" })}
      </p>
    );
  }
  if (state.lines.length === 0) {
    return <p className="setup-log-note">{t("setupLogEmpty")}</p>;
  }
  return (
    <pre className="setup-log" tabIndex={0} aria-label={t("setupLog")}>
      {state.lines.map((l) => l.text).join("\n")}
    </pre>
  );
}
