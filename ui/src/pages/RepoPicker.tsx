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
import {
  filterRepos,
  isLocked,
  LIST_CAP,
  MAX_SHOWN,
  parseRepo,
  repoClash,
  repoKey,
  type RepoClash,
} from "./github";

// ChosenRepo is a repository the form will create the project with: its
// owner/name (what the API takes) and whether to show the lock.
export interface ChosenRepo {
  fullName: string;
  locked: boolean;
}

// SEARCH_DELAY_MS: a search the server answers (a capped list) is asked for
// once typing pauses this long.
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

// useClashText is the sentence for a repository the server would refuse.
function useClashText() {
  const { t } = useTranslation("projects");
  return (c: NonNullable<RepoClash>) =>
    c.kind === "badDir" ? t("repoBadDir") : t("repoSameDir", { name: c.with });
}

type Answer =
  { query: string; items: GitHubRepo[] } | { query: string; code: ErrorCode };

// failed records a list that failed; a lost connection goes to lost.
function failed(
  query: string,
  err: unknown,
  lost: () => void,
  set: (a: Answer) => void,
) {
  const code = errorCode(err);
  if (code === "github_not_connected") lost();
  else set({ query, code });
}

type Option =
  | { kind: "repo"; repo: GitHubRepo; clash: RepoClash }
  | { kind: "byName"; fullName: string; clash: RepoClash };

// RepoPicker chooses repositories from what the GitHub connection reaches:
// a field with the chosen ones as chips and a button that opens a popover
// with a search and the list in the API's order. Each opening reads the list
// once and the search filters it here; only a list the API cut at its cap is
// searched on the server (as typing pauses), meanwhile what was read is
// filtered. The options always match the text in the search. Click or Enter
// adds or removes; one the server would refuse is shown with the reason and
// cannot be added; a search that reads as owner/name and is not listed
// offers to add it by name. Keyboard: arrows move, Enter picks, Escape
// clears the search, then closes; Backspace in an empty search removes the
// last chip (not while held). onDisconnected: the API answered
// github_not_connected; onFailed: whether the list failed otherwise (the
// form then offers adding by name).
export function RepoPicker({
  chosen,
  onAdd,
  onRemove,
  onDisconnected,
  onFailed,
  labelId,
  invalid,
}: {
  chosen: ChosenRepo[];
  onAdd: (r: ChosenRepo) => void;
  onRemove: (fullName: string) => void;
  onDisconnected: () => void;
  onFailed?: (failed: boolean) => void;
  labelId: string;
  invalid?: boolean;
}) {
  const { t } = useTranslation("projects");
  const clashText = useClashText();
  const uid = useId();
  const listId = `${uid}-list`;
  const optId = (i: number) => `${uid}-opt-${i}`;
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // opening counts the openings: each reads the list again (base).
  const [opening, setOpening] = useState(0);
  const [base, setBase] = useState<Answer | null>(null);
  // asked is the search the server is asked for (a capped list only),
  // remote its answer.
  const [asked, setAsked] = useState("");
  const [remote, setRemote] = useState<Answer | null>(null);
  const [remoteRetry, setRemoteRetry] = useState(0);
  const [active, setActive] = useState(0);
  const lost = useRef(onDisconnected);
  const failedRef = useRef(onFailed);
  useEffect(() => {
    lost.current = onDisconnected;
    failedRef.current = onFailed;
  }, [onDisconnected, onFailed]);

  useEffect(() => {
    if (opening === 0) return;
    let live = true;
    const ctl = new AbortController();
    listGitHubRepos("", ctl.signal).then(
      (r) => {
        if (live) setBase({ query: "", items: r.items });
      },
      (err: unknown) => {
        if (live) failed("", err, () => lost.current(), setBase);
      },
    );
    return () => {
      live = false;
      ctl.abort();
    };
  }, [opening]);

  const baseItems = base && "items" in base ? base.items : null;
  const capped = baseItems !== null && baseItems.length >= LIST_CAP;
  const typed = parseRepo(query);
  // text is what is searched: a pasted GitHub address (or owner/name.git,
  // owner/name/) as owner/name, else the search as typed.
  const text = typed ?? query.trim();
  const searching = capped && text !== "";

  // A capped list's search is asked for once typing pauses.
  useEffect(() => {
    if (!open || !capped || text === asked) return;
    const timer = setTimeout(() => setAsked(text), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [open, capped, text, asked]);

  useEffect(() => {
    if (!capped || asked === "") return;
    let live = true;
    // A newer search aborts this one: the server stops reading for it.
    const ctl = new AbortController();
    listGitHubRepos(asked, ctl.signal).then(
      (r) => {
        if (live) setRemote({ query: asked, items: r.items });
      },
      (err: unknown) => {
        if (live) failed(asked, err, () => lost.current(), setRemote);
      },
    );
    return () => {
      live = false;
      ctl.abort();
    };
  }, [capped, asked, remoteRetry]);

  // What the list shows: the base filtered by the search; for a capped list
  // and a search, the server's answer for exactly this text once it is in
  // (until then the base filtered, pending).
  const answered = searching && remote?.query === text ? remote : null;
  let failure: ErrorCode | null = null;
  let pending = false;
  let pool: GitHubRepo[] = [];
  if (base === null) pending = true;
  else if ("code" in base) failure = base.code;
  else if (!searching) pool = base.items;
  else if (answered === null) {
    pending = true;
    pool = base.items;
  } else if ("code" in answered) failure = answered.code;
  else pool = answered.items;
  // The one the typed owner/name names comes first.
  const items = filterRepos(pool, text);
  if (typed) {
    const i = items.findIndex((r) => repoKey(r.fullName) === repoKey(typed));
    if (i > 0) items.unshift(...items.splice(i, 1));
  }

  // The form hears of a failure at once, of its end only once a list is in
  // (not while one loads again: its add-by-name field would go and come).
  useEffect(() => {
    if (failure !== null) failedRef.current?.(true);
    else if (!pending) failedRef.current?.(false);
  }, [failure, pending]);

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

  const shown = items.slice(0, MAX_SHOWN);
  const chosenNames = chosen.map((c) => c.fullName);
  const chosenKeys = new Set(chosenNames.map(repoKey));
  const options: Option[] = shown.map((repo) => ({
    kind: "repo",
    repo,
    clash: chosenKeys.has(repoKey(repo.fullName))
      ? null
      : repoClash(repo.fullName, chosenNames),
  }));
  if (
    typed &&
    !pending &&
    failure === null &&
    !chosenKeys.has(repoKey(typed)) &&
    !pool.some((r) => repoKey(r.fullName) === repoKey(typed))
  ) {
    options.push({
      kind: "byName",
      fullName: typed,
      clash: repoClash(typed, chosenNames),
    });
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
    // Each opening reads the list again.
    setBase(null);
    setRemote(null);
    setAsked("");
    setOpening((o) => o + 1);
    // The search takes the focus once it is rendered.
    setTimeout(() => search.current?.focus(), 0);
  };
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };

  const pick = (o: Option) => {
    if (o.kind === "byName") {
      if (o.clash) return;
      onAdd({ fullName: o.fullName, locked: false });
      setQuery("");
      return;
    }
    if (chosenKeys.has(repoKey(o.repo.fullName))) onRemove(o.repo.fullName);
    else if (!o.clash) {
      onAdd({ fullName: o.repo.fullName, locked: isLocked(o.repo) });
    }
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
        // A held Backspace clears the search and stops there.
        if (query === "" && !e.repeat) {
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
    } else if ((e.key === "Backspace" || e.key === "Delete") && !e.repeat) {
      e.preventDefault();
      removeLast();
    }
  };

  let note: string | null = null;
  if (base === null) note = t("repoLoading");
  else if (!pending && failure === null && items.length === 0 && n === 0) {
    note =
      text !== "" ? t("repoNoMatch", { query: query.trim() }) : t("repoNone");
  } else if (searching && answered && pool.length >= LIST_CAP) {
    note = t("repoCappedMatches", { n: formatNumber(LIST_CAP) });
  } else if (capped && !searching) {
    note = t("repoCapped", { n: formatNumber(LIST_CAP) });
  } else if (items.length > shown.length) {
    note = t("repoShown", {
      shown: formatNumber(shown.length),
      total: formatNumber(items.length),
    });
  }
  const retry = () => {
    if (base && "code" in base) {
      setBase(null);
      setOpening((o) => o + 1);
    } else {
      setRemote(null);
      setRemoteRetry((r) => r + 1);
    }
  };

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
            {searching && pending && (
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
              const cls = [
                "rpk-opt",
                i === current ? "active" : "",
                o.clash ? "off" : "",
              ]
                .filter(Boolean)
                .join(" ");
              const common = {
                id: optId(i),
                role: "option",
                className: cls,
                "aria-disabled": o.clash ? true : undefined,
                // Keep the focus in the search.
                onMouseDown: (e: MouseEvent) => e.preventDefault(),
                onMouseMove: () => i !== current && setActive(i),
                onClick: () => pick(o),
              } as const;
              const why = o.clash && (
                <span className="rpk-ds rpk-why">{clashText(o.clash)}</span>
              );
              if (o.kind === "byName") {
                return (
                  <li key="by-name" {...common} aria-selected={false}>
                    <Icon name="plus" className="rpk-ic" />
                    <span className="rpk-t">
                      <span className="rpk-nm">
                        {t("repoAddByName", { name: o.fullName })}
                      </span>
                      {why}
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
                    {why ||
                      (r.description && (
                        <span className="rpk-ds" title={r.description}>
                          {r.description}
                        </span>
                      ))}
                  </span>
                  {isLocked(r) && <LockMark />}
                </li>
              );
            })}
          </ul>
          {note && <p className="rpk-note">{note}</p>}
          {failure && (
            <div className="rpk-note err" role="alert">
              {t("repoListFailed")} {t(failure, { ns: "errors" })}{" "}
              <button type="button" className="btn sm" onClick={retry}>
                {t("repoRetry")}
              </button>
            </div>
          )}
          <span className="sr" role="status">
            {!pending && failure === null
              ? t("repoMatches", { count: n, n: formatNumber(n) })
              : ""}
          </span>
        </div>
      )}
    </div>
  );
}

// RepoByName adds a public repository typed as owner/name or a github.com
// address, checked in place before it is added: the form, then the server's
// rule for a project's repositories.
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
  const clashText = useClashText();
  const uid = useId();
  const [text, setText] = useState("");
  const [why, setWhy] = useState<string | null>(null);
  const add = () => {
    const full = parseRepo(text);
    if (!full) return setWhy(t("byNameInvalid"));
    if (chosen.some((c) => repoKey(c.fullName) === repoKey(full))) {
      return setWhy(t("byNameTwice"));
    }
    const clash = repoClash(
      full,
      chosen.map((c) => c.fullName),
    );
    if (clash) return setWhy(clashText(clash));
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
          {why}
        </span>
      )}
    </div>
  );
}
