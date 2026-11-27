import { useState } from "react";
import { useTranslation } from "react-i18next";

import { formatDateTime, formatNumber, formatTime } from "../i18n/format";
import { Icon } from "../shell/Icon";
import { Lines, ToolTarget, useToolVerb } from "./Activity";
import type {
  FilesItem,
  NoticeItem,
  ResumedItem,
  TextItem,
  Thinking,
  TodoListItem,
  ToolRow,
  UserItem,
} from "./fold";
import { Markdown } from "./Markdown";

export function UserMessage({ item }: { item: UserItem }) {
  const { t } = useTranslation("timeline");
  const from =
    item.origin === "parent_session"
      ? t("fromParent")
      : item.origin === "child_report"
        ? t("fromChild")
        : null;
  return (
    <div className="u-msg">
      {from && <div className="u-from">{from}</div>}
      <div className="bubble">{item.text}</div>
      <div className="u-meta">{formatTime(new Date(item.ts))}</div>
    </div>
  );
}

export function AssistantText({ item }: { item: TextItem }) {
  return (
    <div className={item.live ? "answer live" : "answer"}>
      <Markdown text={item.text} />
    </div>
  );
}

export function TodoList({ item }: { item: TodoListItem }) {
  const { t } = useTranslation("timeline");
  const done = item.items.filter((i) => i.status === "completed").length;
  return (
    <div className="todo">
      <div className="todo-h">
        <Icon name="list" />
        <span>{t("todos")}</span>
        <span className="n">
          {t("todosDone", {
            done: formatNumber(done),
            total: formatNumber(item.items.length),
          })}
        </span>
      </div>
      <ul>
        {item.items.map((todo, i) => (
          <li
            key={todo.id ?? i}
            className={
              todo.status === "completed"
                ? "done"
                : todo.status === "in_progress"
                  ? "now"
                  : undefined
            }
          >
            <span className="box">
              {todo.status === "completed" && <Icon name="check" />}
            </span>
            {todo.status === "in_progress" && todo.activeText
              ? todo.activeText
              : todo.text}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function FilesCard({ item }: { item: FilesItem }) {
  const { t } = useTranslation("timeline");
  const [open, setOpen] = useState(false);
  const added = item.files.reduce((n, f) => n + f.added, 0);
  const removed = item.files.reduce((n, f) => n + f.removed, 0);
  const count = item.files.length;
  return (
    <div className="card">
      <div className="card-h">
        <button
          className="files-toggle grow"
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          <span className="chev" />
          <span className="card-title">
            {t("filesChanged", { count, n: formatNumber(count) })}
          </span>
          <span className="card-sub mono">{item.repo}</span>
          <span className="counts">
            <Lines added={added} removed={removed} always />
          </span>
        </button>
      </div>
      {open && (
        <ul className="flist">
          {item.files.map((f) => (
            <li key={f.path}>
              <span className="path">{f.path}</span>
              {f.change !== "modified" && (
                <span className="tag">
                  {f.change === "added"
                    ? t("fileChange.added")
                    : f.change === "deleted"
                      ? t("fileChange.deleted")
                      : t("fileChange.renamed")}
                </span>
              )}
              <span className="counts">
                <Lines added={f.added} removed={f.removed} always />
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function ResumedDivider({ item }: { item: ResumedItem }) {
  const { t } = useTranslation("timeline");
  return (
    <div className="divider" role="separator">
      {t("resumedAt", { when: formatDateTime(new Date(item.ts)) })}
    </div>
  );
}

export function NoticeRow({ item }: { item: NoticeItem }) {
  const { t, i18n } = useTranslation("timeline");
  const key = `notices.${item.code}`;
  // The code comes from the server: an untyped lookup, checked first. (The
  // typed t also reads its "_compacted" suffix as an i18next context.)
  const lookup = t as unknown as (k: string, o?: object) => string;
  const text = i18n.exists(key, { ns: "timeline" })
    ? lookup(key, item.params)
    : t("noticeUnknown", { code: item.code });
  return (
    <div className={`resolved note notice-${item.level}`}>
      <Icon name="info" />
      <span className="what">{text}</span>
    </div>
  );
}

// WorkingIndicator: bouncing dots and what the open tool does, or, while the
// agent thinks, "Thinking" and its running token estimate.
export function WorkingIndicator({
  tool,
  thinking,
}: {
  tool?: ToolRow;
  thinking?: Thinking;
}) {
  const { t } = useTranslation("timeline");
  const verb = useToolVerb();
  const showThinking = thinking !== undefined && tool === undefined;
  return (
    <div className="working" role="status">
      <div className="dots" aria-hidden="true">
        <span />
        <span />
        <span />
      </div>
      <span>
        {showThinking ? t("thinkingNow") : t("workingNow")}
        {showThinking && thinking.tokens !== undefined && (
          <span className="faint">
            {t("separator")}
            {t("thinkingTokens", {
              count: thinking.tokens,
              n: formatNumber(thinking.tokens),
            })}
          </span>
        )}
        {tool && (
          <>
            {t("separator")}
            <span className="what">
              {tool.description ?? (
                <>
                  {verb(tool, true)} <ToolTarget row={tool} />
                </>
              )}
            </span>
          </>
        )}
      </span>
    </div>
  );
}
