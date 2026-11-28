import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { errorCode, sandboxAction, sandboxStartable } from "../api";
import type {
  Diff,
  DiffFile,
  ErrorCode,
  SandboxState,
  Worktree,
} from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { Icon } from "../shell/Icon";
import { readStored, STORAGE_KEYS, writeStored } from "../shell/storage";
import {
  clampWidth,
  diffTotals,
  numberHunk,
  PANEL_MIN,
  panelMax,
} from "./diff";
import { useDiff } from "./useDiff";
import "./panel.css";

export const PANEL_ID = "review-panel";

// Counts is "+12 −3", read out as a sentence.
function Counts({ added, removed }: { added: number; removed: number }) {
  const { t } = useTranslation("panel");
  const a = formatNumber(added);
  const r = formatNumber(removed);
  return (
    <span
      className="counts"
      aria-label={t("countsLabel", { added: a, removed: r })}
    >
      <span className="add" aria-hidden="true">
        {t("added", { n: a })}
      </span>{" "}
      <span className="del" aria-hidden="true">
        {t("removed", { n: r })}
      </span>
    </span>
  );
}

const changeKeys = {
  added: "changeAdded",
  deleted: "changeDeleted",
  renamed: "changeRenamed",
} as const;

function FileDiff({ file, id }: { file: DiffFile; id: string }) {
  const { t } = useTranslation("panel");
  const change =
    file.change in changeKeys
      ? changeKeys[file.change as keyof typeof changeKeys]
      : undefined;
  const path =
    file.oldPath && file.oldPath !== file.path
      ? t("renamedFrom", { from: file.oldPath, to: file.path })
      : file.path;
  return (
    <section className="dfile" id={id} aria-label={file.path}>
      <h3 className="dfile-h">
        <span className="path" title={path}>
          {path}
        </span>
        {change && (
          <span
            className={`label-pill ${file.change === "deleted" ? "pill-del" : "pill-ok"}`}
          >
            {t(change)}
          </span>
        )}
        <Counts added={file.added} removed={file.removed} />
      </h3>
      {file.binary ? (
        <p className="dnote">{t("binary")}</p>
      ) : (
        file.hunks.length > 0 && (
          <div className="diff">
            <table>
              <tbody>
                {file.hunks.map((h, hi) => (
                  <HunkRows key={hi} header={h.header} rows={numberHunk(h)} />
                ))}
              </tbody>
            </table>
          </div>
        )
      )}
      {file.truncated && <p className="dnote">{t("fileTruncated")}</p>}
    </section>
  );
}

function HunkRows({
  header,
  rows,
}: {
  header: string;
  rows: ReturnType<typeof numberHunk>;
}) {
  const cls = { ctx: undefined, add: "a", del: "d", note: "n" } as const;
  const sign = { ctx: "", add: "+", del: "−", note: "" } as const;
  return (
    <>
      <tr className="h">
        <td className="ln" />
        <td className="ln" />
        <td className="sg" />
        <td className="code">{header}</td>
      </tr>
      {rows.map((r, i) => (
        <tr key={i} className={cls[r.kind]}>
          <td className="ln">{r.old !== undefined ? formatLine(r.old) : ""}</td>
          <td className="ln">{r.new !== undefined ? formatLine(r.new) : ""}</td>
          <td className="sg">{sign[r.kind]}</td>
          <td className="code">{r.text}</td>
        </tr>
      ))}
    </>
  );
}

// Line numbers without grouping: a gutter reads 1234, not 1'234.
const formatLine = (n: number) => formatNumber(n, { useGrouping: false });

function DiffBody({ diff, branch }: { diff: Diff; branch: string }) {
  const { t } = useTranslation("panel");
  const totals = diffTotals(diff.files);
  const jump = (id: string) => {
    const el = document.getElementById(id);
    if (typeof el?.scrollIntoView !== "function") return;
    const reduce =
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    el.scrollIntoView({ block: "start", behavior: reduce ? "auto" : "smooth" });
  };
  const fileId = (i: number) => `${PANEL_ID}-f${i}`;
  return (
    <>
      <div className="rp-meta">
        <Icon name="repo" />
        <span className="mono rp-repo">{diff.repo}</span>
        <span>{t("versus", { branch, base: diff.baseRef })}</span>
        <span className="hdr-spacer" />
        <Counts added={totals.added} removed={totals.removed} />
      </div>
      {diff.files.length === 0 ? (
        <p className="rp-empty">
          {t("noChangesAgainst", { base: diff.baseRef })}
        </p>
      ) : (
        <>
          <ul className="dfiles" aria-label={t("filesLabel")}>
            {diff.files.map((f, i) => (
              <li key={f.path}>
                <button type="button" onClick={() => jump(fileId(i))}>
                  <Icon name="file" />
                  <span className="path">{f.path}</span>
                  <Counts added={f.added} removed={f.removed} />
                </button>
              </li>
            ))}
          </ul>
          {diff.files.map((f, i) => (
            <FileDiff key={f.path} file={f} id={fileId(i)} />
          ))}
        </>
      )}
      {diff.truncated && <p className="dnote">{t("diffTruncated")}</p>}
    </>
  );
}

function DiffError({
  code,
  projectId,
  sandboxState,
  sandboxBusy = false,
  onRetry,
}: {
  code: ErrorCode;
  projectId: string;
  sandboxState?: SandboxState;
  sandboxBusy?: boolean;
  onRetry: () => void;
}) {
  const { t } = useTranslation("panel");
  const [starting, setStarting] = useState(false);
  const [failed, setFailed] = useState<ErrorCode | null>(null);
  if (code !== "sandbox_unavailable") {
    return (
      <div className="rp-empty" role="alert">
        <p>
          {t("failed")} {t(code, { ns: "errors" })}
        </p>
        <button type="button" className="btn sm" onClick={onRetry}>
          {t("retry")}
        </button>
      </div>
    );
  }
  const start = () => {
    setStarting(true);
    setFailed(null);
    sandboxAction(projectId, "start").catch((err: unknown) => {
      setFailed(errorCode(err));
      setStarting(false);
    });
  };
  // A start helps as the session's sandbox notice says (sandboxStartable):
  // never while an operation runs on the sandbox.
  const sb =
    sandboxState === undefined
      ? undefined
      : { state: sandboxState, busy: sandboxBusy };
  const busy = starting || sandboxBusy;
  const startable = sandboxStartable(sb);
  // Starting only for a start or setup; a busy stop or delete is down.
  const comingUp =
    starting ||
    (sandboxBusy && sandboxState !== "stopping" && sandboxState !== "deleting");
  return (
    <div className="rp-empty" role="status">
      <p>{comingUp ? t("sandboxStarting") : t("sandboxDown")}</p>
      {failed && (
        <p className="rp-err" role="alert">
          {t(failed, { ns: "errors" })}
        </p>
      )}
      {!busy && startable && (
        <button type="button" className="btn sm primary" onClick={start}>
          {t("startSandbox")}
        </button>
      )}
      {!busy && !startable && (
        <button type="button" className="btn sm" onClick={onRetry}>
          {t("retry")}
        </button>
      )}
    </div>
  );
}

// ResizeHandle is the panel's left edge: drag it, or focus it and use Left
// (wider), Right (narrower), Home and End.
function ResizeHandle({
  width,
  max,
  onWidth,
  onDone,
}: {
  width: number;
  max: number;
  onWidth: (w: number) => void;
  onDone: (w: number) => void;
}) {
  const { t } = useTranslation("panel");
  const drag = useRef<{ x: number; w: number; last: number } | null>(null);
  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    drag.current = { x: e.clientX, w: width, last: width };
    e.currentTarget.setPointerCapture?.(e.pointerId);
  };
  const move = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d) return;
    d.last = d.w + (d.x - e.clientX);
    onWidth(d.last);
  };
  const up = () => {
    const d = drag.current;
    drag.current = null;
    if (d) onDone(d.last);
  };
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    const next =
      e.key === "ArrowLeft"
        ? width + 16
        : e.key === "ArrowRight"
          ? width - 16
          : e.key === "Home"
            ? PANEL_MIN
            : e.key === "End"
              ? max
              : null;
    if (next === null) return;
    e.preventDefault();
    onDone(next);
  };
  return (
    <div
      className="rp-resize"
      role="separator"
      aria-orientation="vertical"
      aria-label={t("resize")}
      aria-controls={PANEL_ID}
      aria-valuemin={PANEL_MIN}
      aria-valuemax={max}
      aria-valuenow={width}
      tabIndex={0}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={up}
      onKeyDown={key}
    />
  );
}

// ReviewPanel is the session's diff beside the timeline: one tab per
// repository, the changed files with their counts, the unified hunks. rev
// changes ask for the diff again (the refresh button, a turn's end). Its
// width is the owner's (remembered in this browser); on narrow screens it
// covers the session (CSS), and while it overlays (overlay) it takes focus
// and Escape closes it.
export function ReviewPanel({
  sessionId,
  projectId,
  branch,
  worktrees,
  worktreesError,
  repoId,
  onRepo,
  rev,
  onRefresh,
  onClose,
  sandboxState,
  sandboxBusy = false,
  overlay = false,
  narrow = false,
}: {
  sessionId: string;
  projectId: string;
  branch: string;
  // undefined while the session's worktrees are being read.
  worktrees?: Worktree[];
  worktreesError?: ErrorCode;
  repoId?: string;
  onRepo: (repoId: string) => void;
  rev: number;
  onRefresh: () => void;
  onClose: () => void;
  sandboxState?: SandboxState;
  // sandboxBusy: an operation runs on the sandbox now (Sandbox.busy).
  sandboxBusy?: boolean;
  overlay?: boolean;
  narrow?: boolean;
}) {
  const { t } = useTranslation("panel");
  const panel = useRef<HTMLElement>(null);
  const closeBtn = useRef<HTMLButtonElement>(null);
  const picked = worktrees?.find((w) => w.repoId === repoId) ?? worktrees?.[0];
  const state = useDiff(sessionId, picked?.repoId, rev);

  // The centre's width bounds the panel's.
  const [room, setRoom] = useState(() => window.innerWidth);
  useLayoutEffect(() => {
    const measure = () =>
      setRoom(panel.current?.parentElement?.clientWidth || window.innerWidth);
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, []);
  const [stored, setStored] = useState(() =>
    readStored(STORAGE_KEYS.panelWidth),
  );
  const [dragging, setDragging] = useState<number | null>(null);
  const width = clampWidth(dragging ?? stored, room);
  const setDone = (w: number) => {
    const v = String(clampWidth(w, room));
    setDragging(null);
    setStored(v);
    writeStored(STORAGE_KEYS.panelWidth, v);
  };

  useEffect(() => {
    if (overlay) closeBtn.current?.focus();
  }, [overlay]);

  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (overlay && e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onClose();
    }
  };

  const content = () => {
    const code = worktreesError ?? state.error;
    if (code) {
      return (
        <DiffError
          // A new sandbox state ends a "starting" (as the sandbox notice).
          key={`${code}:${sandboxState ?? ""}:${sandboxBusy}`}
          code={code}
          projectId={projectId}
          sandboxState={sandboxState}
          sandboxBusy={sandboxBusy}
          onRetry={onRefresh}
        />
      );
    }
    if (worktrees !== undefined && worktrees.length === 0) {
      return <p className="rp-empty">{t("noWorktree")}</p>;
    }
    if (state.diff) return <DiffBody diff={state.diff} branch={branch} />;
    return (
      <p className="rp-empty" role="status">
        {t("loading")}
      </p>
    );
  };

  const loading = worktrees === undefined || state.loading;
  const style = { "--panel-w": `${width}px` } as CSSProperties;
  return (
    <aside
      ref={panel}
      id={PANEL_ID}
      className="rpanel"
      aria-label={t("label")}
      style={style}
      onKeyDown={onKeyDown}
    >
      {!narrow && (
        <ResizeHandle
          width={width}
          max={panelMax(room)}
          onWidth={(w) => setDragging(w)}
          onDone={setDone}
        />
      )}
      <div className="rp-h">
        <h2 className="rp-title">{t("diff")}</h2>
        <span className="hdr-spacer" />
        <button
          type="button"
          className="icon-btn"
          aria-label={t("refresh")}
          title={t("refresh")}
          aria-busy={loading}
          onClick={onRefresh}
        >
          <Icon name="reload" className={loading ? "turning" : undefined} />
        </button>
        <button
          ref={closeBtn}
          type="button"
          className="icon-btn"
          aria-label={t("close")}
          title={t("close")}
          onClick={onClose}
        >
          <Icon name="x" />
        </button>
      </div>
      <div className="rp-body">
        {worktrees && worktrees.length > 1 && (
          <div
            className="seg repo-seg"
            role="radiogroup"
            aria-label={t("repository")}
            style={{ "--n": worktrees.length } as CSSProperties}
            onKeyDown={(e) => {
              const step =
                e.key === "ArrowRight" || e.key === "ArrowDown"
                  ? 1
                  : e.key === "ArrowLeft" || e.key === "ArrowUp"
                    ? -1
                    : 0;
              if (step === 0 || !picked) return;
              e.preventDefault();
              const i = worktrees.indexOf(picked);
              const next =
                worktrees[(i + step + worktrees.length) % worktrees.length];
              onRepo(next.repoId);
              e.currentTarget
                .querySelector<HTMLElement>(`[data-repo="${next.repoId}"]`)
                ?.focus();
            }}
          >
            {worktrees.map((w) => (
              <button
                key={w.repoId}
                type="button"
                role="radio"
                data-repo={w.repoId}
                aria-checked={w.repoId === picked?.repoId}
                tabIndex={w.repoId === picked?.repoId ? 0 : -1}
                title={w.repo}
                onClick={() => onRepo(w.repoId)}
              >
                {w.repo.split("/").pop()}
              </button>
            ))}
          </div>
        )}
        {content()}
      </div>
    </aside>
  );
}
