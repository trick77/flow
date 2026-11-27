import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, getSetupLog } from "../api";
import type { ErrorCode, SetupLine } from "../api/types.gen";
import { formatNumber } from "../i18n/format";

// The API answers at most this many lines at once.
const PAGE = 1000;

type State =
  | { kind: "loading" }
  | { kind: "error"; code: ErrorCode }
  | { kind: "ready"; lines: SetupLine[] };

// SetupLog shows the last setup run's log of a project's sandbox in a
// scrollable monospace block. It reads the end first (a failed script's error
// and exit status are there) and earlier lines a page at a time on demand;
// while lines before the first shown exist, a note says the log is truncated.
export function SetupLog({ projectId }: { projectId: string }) {
  const { t } = useTranslation("projects");
  const [state, setState] = useState<State>({ kind: "loading" });
  const [earlier, setEarlier] = useState<"idle" | "loading" | ErrorCode>(
    "idle",
  );

  useEffect(() => {
    let live = true;
    getSetupLog(projectId, 0, PAGE).then(
      (got) => {
        if (live) setState({ kind: "ready", lines: got.lines });
      },
      (err: unknown) => {
        if (live) setState({ kind: "error", code: errorCode(err) });
      },
    );
    return () => {
      live = false;
    };
  }, [projectId]);

  // The block opens at its end; earlier lines put in above keep the lines in
  // view where they were.
  const pre = useRef<HTMLPreElement>(null);
  const height = useRef<number | null>(null);
  const lineCount = state.kind === "ready" ? state.lines.length : 0;
  useLayoutEffect(() => {
    const el = pre.current;
    if (!el) return;
    el.scrollTop =
      height.current === null
        ? el.scrollHeight
        : el.scrollTop + el.scrollHeight - height.current;
    height.current = el.scrollHeight;
  }, [lineCount]);

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
  const lines = state.lines;
  const first = lines[0].n;

  // loadEarlier reads the page that ends right before the first line shown.
  const loadEarlier = () => {
    setEarlier("loading");
    getSetupLog(projectId, Math.max(first - 1 - PAGE, 0)).then(
      (got) => {
        const before = got.lines.filter((l) => l.n < first);
        setState({ kind: "ready", lines: [...before, ...lines] });
        setEarlier("idle");
      },
      (err: unknown) => setEarlier(errorCode(err)),
    );
  };

  return (
    <>
      {first > 1 && (
        <div className="setup-log-note row-inline">
          <span>
            {t("setupLogTruncated", {
              count: lines.length,
              n: formatNumber(lines.length),
            })}
          </span>
          <button
            type="button"
            className="btn sm"
            onClick={loadEarlier}
            disabled={earlier === "loading"}
          >
            {earlier === "loading"
              ? t("setupLogEarlierLoading")
              : t("setupLogEarlier")}
          </button>
        </div>
      )}
      {earlier !== "idle" && earlier !== "loading" && (
        <p className="pcard-err" role="alert">
          {t("setupLogFailed")} {t(earlier, { ns: "errors" })}
        </p>
      )}
      <pre
        ref={pre}
        className="setup-log"
        tabIndex={0}
        aria-label={t("setupLog")}
      >
        {lines.map((l) => l.text).join("\n")}
      </pre>
    </>
  );
}
