import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, replyQuestion } from "../api";
import type { Answer, ErrorCode } from "../api/types.gen";
import type { Provider, Question } from "../contract/events.gen";
import { formatNumber } from "../i18n/format";
import { Icon } from "../shell/Icon";
import { useAgentName } from "./agentName";
import type { QuestionItem } from "./fold";

// answerSummary is what a resolved question shows of its answer: the chosen
// options' labels and any text, else the text typed in the composer.
export function answerSummary(item: QuestionItem): string {
  const r = item.resolved;
  if (!r) return "";
  if (r.answers && r.answers.length > 0) {
    return r.answers
      .map((a) => {
        const q = item.request.questions.find((x) => x.id === a.questionId);
        const labels = a.optionIds.map(
          (id) => q?.options.find((o) => o.id === id)?.label ?? id,
        );
        if (a.text) labels.push(a.text);
        return labels.join(", ");
      })
      .filter((s) => s !== "")
      .join("; ");
  }
  return r.text ?? "";
}

function Resolved({ item }: { item: QuestionItem }) {
  const { t } = useTranslation("timeline");
  const r = item.resolved;
  if (r?.outcome !== "answered") {
    return (
      <div className="resolved denied">
        <Icon name="x" />
        <span className="what">{t("questionExpired")}</span>
      </div>
    );
  }
  const summary = answerSummary(item);
  const inComposer = !(r.answers && r.answers.length > 0) && !!r.text;
  return (
    <div className="resolved">
      <Icon name="check" />
      <span className="what">
        {inComposer ? t("answeredInComposer") : t("answered")}
        {summary !== "" && (
          <>
            {t("separator")}
            {summary}
          </>
        )}
      </span>
    </div>
  );
}

function Choices({
  question,
  name,
  picked,
  other,
  disabled,
  onPick,
  onOther,
  showText,
}: {
  question: Question;
  // The inputs' name: unique per card and question.
  name: string;
  picked: readonly string[];
  other: string;
  disabled: boolean;
  onPick: (optionId: string) => void;
  onOther: (text: string) => void;
  // A card with several questions names each above its options.
  showText: boolean;
}) {
  const { t } = useTranslation("timeline");
  const multi = question.multiSelect;
  const label = question.header ?? question.text;
  return (
    <div className="q-block">
      {showText && <p className="q-text">{question.text}</p>}
      <div
        className="q-opts"
        role={multi ? "group" : "radiogroup"}
        aria-label={label}
      >
        {question.options.map((o) => (
          <label className="q-opt" key={o.id}>
            <input
              type={multi ? "checkbox" : "radio"}
              name={name}
              value={o.id}
              checked={picked.includes(o.id)}
              disabled={disabled}
              onChange={() => onPick(o.id)}
            />
            <span className={multi ? "box" : "radio"} aria-hidden="true">
              {multi && <Icon name="check" />}
            </span>
            <span className="grow">
              <span className="t">{o.label}</span>
              {o.description && <span className="d">{o.description}</span>}
            </span>
          </label>
        ))}
        <input
          className="q-other"
          type="text"
          aria-label={`${label}: ${t("questionOther")}`}
          placeholder={t("questionOther")}
          value={other}
          disabled={disabled}
          onChange={(e) => onOther(e.target.value)}
        />
      </div>
    </div>
  );
}

// QuestionCard shows the agent's questions with their options (one or
// several per question, as asked) and an optional free answer; Send answer
// replies with them. Answer in the composer hands over to the composer,
// whose next message answers the question as text. Once resolved, or once
// its turn ended unanswered, it shrinks to one line.
export function QuestionCard({
  item,
  sessionId,
  provider,
  onAnswerInComposer,
}: {
  item: QuestionItem;
  sessionId: string;
  provider?: Provider;
  onAnswerInComposer?: () => void;
}) {
  const { t } = useTranslation("timeline");
  const agent = useAgentName(provider);
  const [picked, setPicked] = useState<Record<string, string[]>>({});
  const [other, setOther] = useState<Record<string, string>>({});
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  if (!item.open) return <Resolved item={item} />;

  const qs = item.request.questions;
  const pick = (q: Question, optionId: string) =>
    setPicked((p) => {
      const now = p[q.id] ?? [];
      if (!q.multiSelect) return { ...p, [q.id]: [optionId] };
      return {
        ...p,
        [q.id]: now.includes(optionId)
          ? now.filter((x) => x !== optionId)
          : [...now, optionId],
      };
    });
  const answers: Answer[] = qs.map((q) => {
    const text = (other[q.id] ?? "").trim();
    return {
      questionId: q.id,
      optionIds: picked[q.id] ?? [],
      ...(text === "" ? {} : { text }),
    };
  });
  const complete = answers.every(
    (a) => a.optionIds.length > 0 || a.text !== undefined,
  );

  const send = () => {
    if (!complete || sent) return;
    setSent(true);
    setError(null);
    replyQuestion(sessionId, item.request.requestId, { answers }).catch(
      (err: unknown) => {
        setSent(false);
        setError(errorCode(err));
      },
    );
  };

  const title =
    qs.length === 1
      ? qs[0].text
      : t("questionsTitle", {
          count: qs.length,
          n: formatNumber(qs.length),
          agent,
        });
  return (
    <div className="card question" data-testid={item.key}>
      <div className="card-h">
        <Icon name="help" className="file-ic" />
        <div className="grow">
          <div className="card-title">{title}</div>
        </div>
        <span className="label-pill pill-cold">{t("question")}</span>
      </div>
      {qs.map((q) => (
        <Choices
          key={q.id}
          question={q}
          name={`${item.key}:${q.id}`}
          picked={picked[q.id] ?? []}
          other={other[q.id] ?? ""}
          disabled={sent}
          onPick={(id) => pick(q, id)}
          onOther={(text) => setOther((o) => ({ ...o, [q.id]: text }))}
          showText={qs.length > 1}
        />
      ))}
      {error && (
        <p className="card-error q-error" role="alert">
          {t(error, { ns: "errors" })}
        </p>
      )}
      <div className="card-foot">
        <button
          className="btn primary"
          type="button"
          disabled={!complete || sent}
          onClick={send}
        >
          {t("sendAnswer")}
        </button>
        {onAnswerInComposer && (
          <button
            className="btn ghost"
            type="button"
            disabled={sent}
            onClick={onAnswerInComposer}
          >
            {t("answerInComposer")}
          </button>
        )}
      </div>
    </div>
  );
}
