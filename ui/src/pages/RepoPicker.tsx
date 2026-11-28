import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { errorCode, listGitHubRepos } from "../api";
import type { ErrorCode, GitHubRepo } from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { Icon } from "../shell";
import { isLocked, LIST_CAP, MAX_SHOWN, parseRepo, repoKey } from "./github";

// ChosenRepo is a repository the form will create the project with: its
// owner/name (what the API takes) and whether to show the lock.
export interface ChosenRepo {
  fullName: string;
  locked: boolean;
}

// SEARCH_DELAY_MS: the search asks the server once typing pauses this long.
export const SEARCH_DELAY_MS = 250;

// LockMark is the mark of a repository that is not public; public ones carry
// none.
export function LockMark() {
  const { t } = useTranslation("projects");
  return (
    <span
      className="rpk-lock"
      role="img"
      aria-label={t("repoNotPublic")}
      title={t("repoNotPublic")}
    >
      <Icon name="lock" />
    </span>
  );
}

// RepoChips are the chosen repositories, each removable.
export function RepoChips({
  chosen,
  onRemove,
}: {
  chosen: ChosenRepo[];
  onRemove: (fullName: string) => void;
}) {
  const { t } = useTranslation("projects");
  if (chosen.length === 0) return null;
  return (
    <ul className="rpk-chips" aria-label={t("repoChosen")}>
      {chosen.map((r) => (
        <li key={repoKey(r.fullName)} className="chip rpk-chip">
          <Icon name="repo" />
          <span className="mono" title={r.fullName}>
            {r.fullName}
          </span>
          {r.locked && <LockMark />}
          <button
            type="button"
            className="rpk-x"
            aria-label={t("repoRemove", { name: r.fullName })}
            onClick={() => onRemove(r.fullName)}
          >
            <Icon name="x" />
          </button>
        </li>
      ))}
    </ul>
  );
}

type ListState =
  | { phase: "loading"; items: GitHubRepo[] | null }
  | { phase: "ready"; items: GitHubRepo[] }
  | { phase: "failed"; code: ErrorCode };

type Answer =
  { query: string; items: GitHubRepo[] } | { query: string; code: ErrorCode };

type Option =
  { kind: "repo"; repo: GitHubRepo } | { kind: "byName"; fullName: string };

// RepoPicker chooses repositories from what the GitHub connection reaches:
// a field with the chosen ones as chips and a button that opens a popover
// with a search (asked of the server as typing pauses) and the list in the
// API's order. Click or Enter adds or removes; a search that reads as
// owner/name and is not listed offers to add it by name. Keyboard: arrows
// move, Enter picks, Escape clears the search, then closes; Backspace in an
// empty search removes the last chip. onDisconnected: the API answered
// github_not_connected (the connection went or expired).
export function RepoPicker({
  chosen,
  onAdd,
  onRemove,
  onDisconnected,
  labelId,
  invalid,
}: {
  chosen: ChosenRepo[];
  onAdd: (r: ChosenRepo) => void;
  onRemove: (fullName: string) => void;
  onDisconnected: () => void;
  labelId: string;
  invalid?: boolean;
}) {
  const { t } = useTranslation("projects");
  const uid = useId();
  const listId = `${uid}-list`;
  const optId = (i: number) => `${uid}-opt-${i}`;
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // asked is the search the server is asked for (null: not yet opened);
  // answer is its last answer, last the last list it gave.
  const [asked, setAsked] = useState<string | null>(null);
  const [answer, setAnswer] = useState<Answer | null>(null);
  const [last, setLast] = useState<GitHubRepo[] | null>(null);
  const [active, setActive] = useState(0);
  const [retry, setRetry] = useState(0);
  const lost = useRef(onDisconnected);
  useEffect(() => {
    lost.current = onDisconnected;
  }, [onDisconnected]);

  // A change of the search is asked for once typing pauses.
  useEffect(() => {
    if (!open || asked === null) return;
    const q = query.trim();
    if (q === asked) return;
    const timer = setTimeout(() => setAsked(q), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [open, query, asked]);

  useEffect(() => {
    if (asked === null) return;
    let live = true;
    listGitHubRepos(asked).then(
      (r) => {
        if (!live) return;
        setLast(r.items);
        setAnswer({ query: asked, items: r.items });
      },
      (err: unknown) => {
        if (!live) return;
        const code = errorCode(err);
        if (code === "github_not_connected") lost.current();
        else setAnswer({ query: asked, code });
      },
    );
    return () => {
      live = false;
    };
  }, [asked, retry]);

  // Typed but not asked yet (the pause before the request) counts as
  // loading: the last answer is for other text, so it neither says "no
  // match" nor offers to add the text by name.
  const loading =
    answer === null ||
    answer.query !== asked ||
    (asked !== null && query.trim() !== asked);
  const failed = !loading && "code" in answer ? answer.code : null;
  const list: ListState = failed
    ? { phase: "failed", code: failed }
    : loading
      ? { phase: "loading", items: last }
      : { phase: "ready", items: last ?? [] };

  // A click or a focus outside closes the popover.
  useEffect(() => {
    if (!open) return;
    const away = (e: Event) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    document.addEventListener("focusin", away);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("focusin", away);
    };
  }, [open]);

  const items = list.phase === "failed" ? [] : (list.items ?? []);
  const shown = items.slice(0, MAX_SHOWN);
  const chosenKeys = new Set(chosen.map((c) => repoKey(c.fullName)));
  const typed = parseRepo(query);
  const options: Option[] = shown.map((repo) => ({ kind: "repo", repo }));
  if (
    typed &&
    list.phase === "ready" &&
    !chosenKeys.has(repoKey(typed)) &&
    !items.some((r) => repoKey(r.fullName) === repoKey(typed))
  ) {
    options.push({ kind: "byName", fullName: typed });
  }
  const n = options.length;
  const current = n === 0 ? -1 : Math.min(active, n - 1);

  useEffect(() => {
    if (!open || current < 0) return;
    document
      .getElementById(optId(current))
      ?.scrollIntoView?.({ block: "nearest" });
    // optId derives from uid, stable for the component's life.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, current]);

  const openPicker = () => {
    setOpen(true);
    setActive(0);
    // The first opening asks at once.
    if (asked === null) setAsked(query.trim());
    // The search takes the focus once it is rendered.
    setTimeout(() => search.current?.focus(), 0);
  };
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };

  const pick = (o: Option) => {
    if (o.kind === "byName") {
      onAdd({ fullName: o.fullName, locked: false });
      setQuery("");
      return;
    }
    if (chosenKeys.has(repoKey(o.repo.fullName))) onRemove(o.repo.fullName);
    else onAdd({ fullName: o.repo.fullName, locked: isLocked(o.repo) });
  };

  const removeLast = () => {
    const last = chosen[chosen.length - 1];
    if (last) onRemove(last.fullName);
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        if (n > 0) setActive((current + 1) % n);
        break;
      case "ArrowUp":
        e.preventDefault();
        if (n > 0) setActive((current - 1 + n) % n);
        break;
      case "Home":
        if (n > 0) {
          e.preventDefault();
          setActive(0);
        }
        break;
      case "End":
        if (n > 0) {
          e.preventDefault();
          setActive(n - 1);
        }
        break;
      case "Enter":
        // Never submits the form from the search.
        e.preventDefault();
        if (current >= 0) pick(options[current]);
        break;
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        if (query !== "") {
          setQuery("");
          setActive(0);
        } else close(true);
        break;
      case "Backspace":
        if (query === "") {
          e.preventDefault();
          removeLast();
        }
        break;
      case "Tab":
        setOpen(false);
        break;
    }
  };

  const onTriggerKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      openPicker();
    } else if (e.key === "Backspace" || e.key === "Delete") {
      e.preventDefault();
      removeLast();
    }
  };

  const q = query.trim();
  let note: string | null = null;
  if (list.phase === "loading" && list.items === null) note = t("repoLoading");
  else if (list.phase === "ready" && items.length === 0 && n === 0) {
    note = q !== "" ? t("repoNoMatch", { query: q }) : t("repoNone");
  } else if (items.length >= LIST_CAP) {
    note = t("repoCapped", { n: formatNumber(LIST_CAP) });
  } else if (items.length > shown.length) {
    note = t("repoShown", {
      shown: formatNumber(shown.length),
      total: formatNumber(items.length),
    });
  }

  return (
    <div className="rpk" ref={root}>
      <div
        className={open ? "rpk-field open" : "rpk-field"}
        data-invalid={invalid || undefined}
      >
        <RepoChips chosen={chosen} onRemove={onRemove} />
        <button
          ref={trigger}
          type="button"
          className="rpk-trigger"
          id={`${uid}-trigger`}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-labelledby={`${labelId} ${uid}-trigger`}
          onClick={() => (open ? close(false) : openPicker())}
          onKeyDown={onTriggerKey}
        >
          <Icon name={chosen.length === 0 ? "search" : "plus"} />
          <span className="grow">
            {chosen.length === 0 ? t("pickRepos") : t("pickMore")}
          </span>
          <Icon name="chevDown" className="rpk-chev" />
        </button>
      </div>
      {open && (
        <div className="rpk-pop">
          <div className="rpk-search">
            <Icon name="search" />
            <input
              ref={search}
              type="text"
              role="combobox"
              autoComplete="off"
              spellCheck={false}
              aria-label={t("repoSearch")}
              aria-expanded="true"
              aria-controls={listId}
              aria-autocomplete="list"
              aria-activedescendant={current >= 0 ? optId(current) : undefined}
              placeholder={t("repoSearchPlaceholder")}
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setActive(0);
              }}
              onKeyDown={onSearchKey}
            />
            {list.phase === "loading" && list.items !== null && (
              <span className="rpk-busy">{t("repoSearching")}</span>
            )}
          </div>
          <ul
            id={listId}
            role="listbox"
            aria-multiselectable="true"
            aria-label={t("repoList")}
            className="rpk-list"
          >
            {options.map((o, i) => {
              const cls = i === current ? "rpk-opt active" : "rpk-opt";
              const common = {
                id: optId(i),
                role: "option",
                className: cls,
                // Keep the focus in the search.
                onMouseDown: (e: MouseEvent) => e.preventDefault(),
                onMouseMove: () => i !== current && setActive(i),
                onClick: () => pick(o),
              } as const;
              if (o.kind === "byName") {
                return (
                  <li key="by-name" {...common} aria-selected={false}>
                    <Icon name="plus" className="rpk-ic" />
                    <span className="rpk-t">
                      <span className="rpk-nm">
                        {t("repoAddByName", { name: o.fullName })}
                      </span>
                    </span>
                  </li>
                );
              }
              const r = o.repo;
              const on = chosenKeys.has(repoKey(r.fullName));
              return (
                <li key={r.fullName} {...common} aria-selected={on}>
                  <Icon name={on ? "check" : "repo"} className="rpk-ic" />
                  <span className="rpk-t">
                    <span className="rpk-nm mono" title={r.fullName}>
                      {r.fullName}
                    </span>
                    {r.description && (
                      <span className="rpk-ds" title={r.description}>
                        {r.description}
                      </span>
                    )}
                  </span>
                  {isLocked(r) && <LockMark />}
                </li>
              );
            })}
          </ul>
          {note && <p className="rpk-note">{note}</p>}
          {list.phase === "failed" && (
            <div className="rpk-note err" role="alert">
              {t("repoListFailed")} {t(list.code, { ns: "errors" })}{" "}
              <button
                type="button"
                className="btn sm"
                onClick={() => {
                  setAnswer(null);
                  setRetry((r) => r + 1);
                }}
              >
                {t("repoRetry")}
              </button>
            </div>
          )}
          <span className="sr" role="status">
            {list.phase === "ready"
              ? t("repoMatches", { count: n, n: formatNumber(n) })
              : ""}
          </span>
        </div>
      )}
    </div>
  );
}

// RepoByName adds a public repository typed as owner/name or a github.com
// address, checked in place before it is added.
export function RepoByName({
  chosen,
  onAdd,
  label,
}: {
  chosen: ChosenRepo[];
  onAdd: (r: ChosenRepo) => void;
  label: string;
}) {
  const { t } = useTranslation("projects");
  const uid = useId();
  const [text, setText] = useState("");
  const [why, setWhy] = useState<"byNameInvalid" | "byNameTwice" | null>(null);
  const add = () => {
    const full = parseRepo(text);
    if (!full) return setWhy("byNameInvalid");
    if (chosen.some((c) => repoKey(c.fullName) === repoKey(full))) {
      return setWhy("byNameTwice");
    }
    onAdd({ fullName: full, locked: false });
    setText("");
    setWhy(null);
  };
  return (
    <div className="field rpk-byname">
      <label htmlFor={`${uid}-in`}>{label}</label>
      <div className="row-inline">
        <input
          id={`${uid}-in`}
          className="input mono"
          type="text"
          autoComplete="off"
          spellCheck={false}
          placeholder={t("byNamePlaceholder")}
          value={text}
          aria-invalid={why !== null || undefined}
          aria-describedby={why ? `${uid}-err` : undefined}
          onChange={(e) => {
            setText(e.target.value);
            setWhy(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              // Adds the repository; the form is submitted by its button.
              e.preventDefault();
              add();
            }
          }}
        />
        <button
          type="button"
          className="btn"
          disabled={text.trim() === ""}
          onClick={add}
        >
          {t("byNameAdd")}
        </button>
      </div>
      {why && (
        <span className="err" id={`${uid}-err`} role="alert">
          {t(why)}
        </span>
      )}
    </div>
  );
}
