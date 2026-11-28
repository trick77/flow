import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import type { SandboxState } from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { projectBadge } from "./badge";
import { MenuButton } from "./drawer";
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
  // ended: this edit was finished already. A blur that reaches the field
  // after Escape or Enter, before the re-render, still sees the typed draft
  // (a browser may blur a focused field it removes); only the first finish
  // counts.
  const ended = useRef(false);
  if (!onRename) return <h2 className="ttl">{title}</h2>;

  const done = (save: boolean) => {
    if (ended.current) return;
    ended.current = true;
    const next = draft?.trim() ?? "";
    setDraft(null);
    if (save && next !== "" && next !== title) onRename(next);
  };
  const edit = () => {
    ended.current = false;
    setDraft(title);
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
        onClick={edit}
      >
        {title}
      </button>
    </h2>
  );
}

// sandboxKeys name a sandbox's state as the header chip and the sidebar's
// project dot say it.
export const sandboxKeys = {
  none: "sandboxChipNone",
  setup: "sandboxChipSetup",
  warm: "sandboxChipWarm",
  stopping: "sandboxChipStopping",
  stopped: "sandboxChipStopped",
  failed: "sandboxChipFailed",
  deleting: "sandboxChipDeleting",
} as const satisfies Record<SandboxState, string>;

// sandboxTone is the chip dot's colour: running, on its way (an operation
// runs, busy), or not running. A setup or stop left behind is not running.
export function sandboxTone(
  state: SandboxState,
  busy = false,
): "warm" | "busy" | "down" {
  if (busy || state === "deleting") return "busy";
  if (state === "warm") return "warm";
  return "down";
}

// sandboxLabelKey names a sandbox as the chip and the sidebar's project dot
// say it: setup and stopping with nothing running did not finish, as the
// project card says.
export function sandboxLabelKey(state: SandboxState, busy: boolean) {
  if (!busy && state === "setup") return "sandboxChipSetupStale";
  if (!busy && state === "stopping") return "sandboxChipStopStale";
  return sandboxKeys[state] ?? "sandboxChipNone";
}

// SandboxChip is the project sandbox's state; the text hides on tight
// headers (CSS), the dot and the label stay.
export function SandboxChip({
  state,
  busy = false,
}: {
  state: SandboxState;
  busy?: boolean;
}) {
  const { t } = useTranslation("shell");
  const label = t(sandboxLabelKey(state, busy));
  return (
    <span
      className={`chip sbx-chip tone-${sandboxTone(state, busy)}`}
      title={label}
    >
      <span className="chip-dot" aria-hidden="true" />
      <span className="txt" aria-hidden="true">
        {label}
      </span>
      <span className="sr">{label}</span>
    </span>
  );
}

// RepoEntry is one repository of a session as the repos menu lists it: from
// the session's worktrees when they could be read, else just its name.
export interface RepoEntry {
  repoId?: string;
  name: string;
  added?: number;
  removed?: number;
  path?: string;
}

// ReposMenu is the chip of a session working in several repositories and
// its menu: each repository with its counts and worktree; choosing one opens
// its diff (onOpen). Up and Down move, Escape closes.
export function ReposMenu({
  branch,
  repos,
  onOpen,
}: {
  branch: string;
  repos: RepoEntry[];
  onOpen?: (repoId: string) => void;
}) {
  const { t } = useTranslation("shell");
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const n = formatNumber(repos.length);

  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      button.current?.focus();
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = [
      ...(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ??
        []),
    ];
    if (items.length === 0) return;
    e.preventDefault();
    const i = items.indexOf(document.activeElement as HTMLElement);
    const step = e.key === "ArrowDown" ? 1 : -1;
    items[(i + step + items.length) % items.length].focus();
  };

  return (
    <div className="repos-wrap" ref={wrap} onKeyDown={onKeyDown}>
      <button
        ref={button}
        type="button"
        className="chip chip-btn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("reposLabel", { count: repos.length, n })}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="stack" />
        <span className="txt">
          {t("repoCount", { count: repos.length, n })}
        </span>
        <Icon name="chevDown" />
      </button>
      {open && (
        <div
          className="pop down repos-pop"
          role="menu"
          aria-label={t("reposMenu")}
          ref={menu}
        >
          <div className="pop-h">
            {t("reposOnBranch")} <span className="mono">{branch}</span>
          </div>
          {repos.map((r) => {
            const id = r.repoId;
            const counts =
              r.added !== undefined || r.removed !== undefined
                ? `+${formatNumber(r.added ?? 0)} −${formatNumber(r.removed ?? 0)}`
                : undefined;
            return (
              <button
                key={id ?? r.name}
                type="button"
                className="opt"
                role="menuitem"
                aria-disabled={!id || !onOpen ? true : undefined}
                onClick={() => {
                  if (!id || !onOpen) return;
                  setOpen(false);
                  onOpen(id);
                }}
              >
                <span className="mono-badge" aria-hidden="true">
                  {projectBadge(r.name.split("/").pop() ?? r.name)}
                </span>
                <span className="grow">
                  <span className="t">
                    <span className="nm">{r.name}</span>
                    {counts && <span className="st">{counts}</span>}
                  </span>
                  <span className="d">{r.path ?? t("repoNoWorktree")}</span>
                </span>
              </button>
            );
          })}
          {onOpen && repos.some((r) => r.repoId) && (
            <>
              <div className="pop-sep" role="separator" />
              <div className="pop-h">{t("reposHint")}</div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// Header: the crumb "project / title", the branch chip, the repos menu and
// the sandbox chip and, at the right, the session toggles (actions). With
// onRename the title can be edited in place. On narrow screens the menu
// button opening the sidebar comes first.
export function Header({
  project,
  title,
  branch,
  actions,
  onRename,
  sandbox,
  sandboxBusy,
  repos,
  onOpenRepo,
}: {
  project?: string;
  title: string;
  branch: string;
  actions?: ReactNode;
  onRename?: (title: string) => void;
  sandbox?: SandboxState;
  // sandboxBusy: an operation runs on the sandbox now (Sandbox.busy).
  sandboxBusy?: boolean;
  // repos: the session's repositories; the menu shows for more than one.
  repos?: RepoEntry[];
  onOpenRepo?: (repoId: string) => void;
}) {
  const { t } = useTranslation("shell");
  return (
    <header className="hdr">
      <MenuButton />
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
        <span className="chip branch-chip" title={t("branch")}>
          <Icon name="branch" />
          <span className="mono">{branch}</span>
        </span>
      )}
      {repos && repos.length > 1 && (
        <ReposMenu branch={branch} repos={repos} onOpen={onOpenRepo} />
      )}
      {sandbox && <SandboxChip state={sandbox} busy={sandboxBusy} />}
      {actions}
    </header>
  );
}
