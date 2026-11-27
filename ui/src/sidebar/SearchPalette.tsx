import {
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useTranslation } from "react-i18next";

import type { Project, SessionSummary } from "../api/types.gen";
import { formatNumber } from "../i18n/format";
import { AgentPill } from "../shell/AgentPill";
import { projectBadge } from "../shell/badge";
import { Icon } from "../shell/Icon";
import { searchSessions, type Part } from "./search";
import { statusClass, statusKey } from "./status";

// The list grows and shrinks with its content up to this share of the
// viewport, then scrolls.
const MAX_SHARE = 0.6;

const FOCUSABLE =
  'a[href], button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex]:not([tabindex="-1"])';

const contentHeight = (el: HTMLElement) => el.scrollHeight;

function Marked({ parts }: { parts: Part[] }) {
  return (
    <>
      {parts.map((p, i) =>
        p.hit ? <mark key={i}>{p.text}</mark> : <span key={i}>{p.text}</span>,
      )}
    </>
  );
}

// SearchPalette is the session search: a modal over the app with one input
// and the matching sessions. Up and Down move, Enter opens, Escape or a click
// beside it closes; focus stays inside while it is open and goes back to
// where it was when it closes. measure is the list content's height, a
// seam for tests (jsdom lays nothing out).
export function SearchPalette({
  sessions,
  projects,
  onSelect,
  onClose,
  measure = contentHeight,
}: {
  sessions: readonly SessionSummary[];
  projects: readonly Project[];
  onSelect: (id: string) => void;
  onClose: () => void;
  measure?: (el: HTMLElement) => number;
}) {
  const { t } = useTranslation("sidebar");
  const id = useId();
  const titleId = `${id}-title`;
  const listId = `${id}-list`;
  const optionId = (i: number) => `${id}-opt-${i}`;
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [height, setHeight] = useState<number>();
  const dialog = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const content = useRef<HTMLDivElement>(null);

  const names = useMemo(
    () => new Map(projects.map((p) => [p.id, p.name])),
    [projects],
  );
  const { items: results, total } = useMemo(
    () => searchSessions(sessions, (pid) => names.get(pid) ?? "", query),
    [sessions, names, query],
  );
  const current = Math.min(active, Math.max(results.length - 1, 0));

  // Focus moves in on open and back to the trigger on close.
  useEffect(() => {
    const before = document.activeElement;
    input.current?.focus();
    return () => {
      if (before instanceof HTMLElement && before.isConnected) before.focus();
    };
  }, []);

  // The list's height follows its content, clamped; CSS animates the change.
  useLayoutEffect(() => {
    const fit = () => {
      if (!content.current) return;
      const max = Math.round(window.innerHeight * MAX_SHARE);
      setHeight(Math.min(measure(content.current), max));
    };
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [results, measure]);

  const activeId = results.length > 0 ? optionId(current) : undefined;
  // The active option stays in view.
  useEffect(() => {
    if (activeId) {
      document.getElementById(activeId)?.scrollIntoView?.({ block: "nearest" });
    }
  }, [activeId]);

  const choose = (i: number) => {
    const r = results[i];
    if (!r) return;
    onSelect(r.session.id);
    onClose();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        onClose();
        return;
      case "ArrowDown":
      case "ArrowUp": {
        e.preventDefault();
        if (results.length === 0) return;
        const step = e.key === "ArrowDown" ? 1 : -1;
        setActive((current + step + results.length) % results.length);
        return;
      }
      case "Enter":
        if (e.nativeEvent.isComposing) return;
        e.preventDefault();
        choose(current);
        return;
      case "Tab": {
        // The trap: Tab cycles through the dialog's own controls.
        const items = Array.from(
          dialog.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [],
        );
        e.preventDefault();
        if (items.length === 0) return;
        const at = items.indexOf(document.activeElement as HTMLElement);
        const next = e.shiftKey
          ? (at - 1 + items.length) % items.length
          : (at + 1) % items.length;
        items[next].focus();
        return;
      }
    }
  };

  const typed = query.trim() !== "";
  return (
    <div
      className="pal-scrim"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className="pal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        ref={dialog}
        onKeyDown={onKeyDown}
        // A click on the dialog's text or list keeps focus in the input;
        // focus on the body would leave the dialog deaf to its keys.
        onMouseDown={(e) => {
          if (e.target !== input.current) e.preventDefault();
        }}
      >
        <h2 className="sr" id={titleId}>
          {t("search")}
        </h2>
        <div className="pal-in">
          <Icon name="search" />
          <input
            ref={input}
            type="text"
            role="combobox"
            aria-expanded={results.length > 0}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={activeId}
            aria-label={t("search")}
            placeholder={t("searchPlaceholder")}
            autoComplete="off"
            spellCheck={false}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActive(0);
            }}
          />
          <kbd className="pal-kbd">{t("searchEsc")}</kbd>
        </div>
        <div className="pal-body" style={{ height }}>
          <div ref={content} className="pal-content">
            <div className="pal-h" aria-hidden="true">
              {!typed
                ? t("searchRecent")
                : total > results.length
                  ? t("searchMatchesFirst", {
                      shown: formatNumber(results.length),
                      n: formatNumber(total),
                    })
                  : t("searchMatches", {
                      count: total,
                      n: formatNumber(total),
                    })}
            </div>
            {results.length === 0 && (
              <p className="pal-empty" role="status">
                {typed ? t("searchNone") : t("searchNoSessions")}
              </p>
            )}
            <ul
              className="pal-list"
              role="listbox"
              id={listId}
              aria-label={typed ? t("searchMatchesLabel") : t("searchRecent")}
            >
              {results.map((r, i) => (
                <li
                  key={r.session.id}
                  id={optionId(i)}
                  role="option"
                  aria-selected={i === current}
                  className="pal-opt"
                  onMouseMove={() => i !== current && setActive(i)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => choose(i)}
                >
                  <span className="mono-badge" aria-hidden="true">
                    {projectBadge(names.get(r.session.projectId) ?? "")}
                  </span>
                  <span className="pal-main">
                    <span className="pal-title">
                      <Marked parts={r.title} />
                    </span>
                    <span className="pal-sub">
                      <span className="pal-proj">
                        <Marked parts={r.project} />
                      </span>
                      <Icon name="branch" />
                      <span className="pal-branch">
                        <Marked parts={r.branch} />
                      </span>
                    </span>
                  </span>
                  <span className="pal-right">
                    <span className={`status ${statusClass(r.session.status)}`}>
                      {t(statusKey(r.session.status))}
                    </span>
                    <AgentPill provider={r.session.provider} />
                  </span>
                </li>
              ))}
            </ul>
          </div>
        </div>
      </div>
    </div>
  );
}
