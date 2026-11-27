import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, replyPlan } from "../api";
import type { ErrorCode, PlanAnswer } from "../api/types.gen";
import { Picker, type PickerOption } from "../composer/Picker";
import {
  PERMISSION_MODE_VALUES,
  type PermissionMode,
} from "../contract/events.gen";
import { Icon } from "../shell/Icon";
import { modeNames } from "./agentName";
import type { PlanItem } from "./fold";
import { Markdown } from "./Markdown";

// continueModes are the modes an approved plan can continue in: every mode
// but plan itself, in the catalog's order, those above the cap left out.
export function continueModes(
  modes: readonly PermissionMode[],
  cap?: PermissionMode,
): PermissionMode[] {
  const capAt = cap ? modes.indexOf(cap) : -1;
  return modes.filter(
    (m, i) => m !== "plan" && m in modeNames && (capAt < 0 || i <= capAt),
  );
}

// defaultContinueMode: the session's mode when it is not plan, else accept
// edits, else the most careful one offered.
export function defaultContinueMode(
  offered: readonly PermissionMode[],
  current?: PermissionMode,
): PermissionMode | undefined {
  if (current && offered.includes(current)) return current;
  if (offered.includes("accept_edits")) return "accept_edits";
  return offered[0];
}

function Resolved({ item }: { item: PlanItem }) {
  const { t } = useTranslation("timeline");
  const r = item.resolved;
  if (r?.decision === "approved") {
    return (
      <div className="resolved">
        <Icon name="check" />
        <span className="what">
          {t("planApproved")}
          {r.mode && r.mode in modeNames && (
            <>
              {t("separator")}
              {t("planApprovedMode", {
                mode: t(modeNames[r.mode], { ns: "composer" }),
              })}
            </>
          )}
        </span>
      </div>
    );
  }
  if (r?.decision === "rejected") {
    return (
      <div className="resolved note">
        <Icon name="map" />
        <span className="what">
          {t("planRejected")}
          {r.feedback && (
            <>
              {t("separator")}
              {r.feedback}
            </>
          )}
        </span>
      </div>
    );
  }
  return (
    <div className="resolved denied">
      <Icon name="x" />
      <span className="what">{t("planExpired")}</span>
    </div>
  );
}

// PlanCard shows the plan the agent presents, as Markdown, with Approve (in
// the mode the session continues in) and Keep planning (with feedback for
// the agent). Once resolved, or once its turn ended undecided, it shrinks to
// one line.
export function PlanCard({
  item,
  sessionId,
  modes = PERMISSION_MODE_VALUES,
  modeCap,
  mode,
}: {
  item: PlanItem;
  sessionId: string;
  // The catalog's modes, least to most permissive; the cap bounds them.
  modes?: readonly PermissionMode[];
  modeCap?: PermissionMode;
  // The session's mode now.
  mode?: PermissionMode;
}) {
  const { t } = useTranslation("timeline");
  const offered = continueModes(modes, modeCap);
  const [chosen, setChosen] = useState<PermissionMode>();
  const [rejecting, setRejecting] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<ErrorCode | null>(null);
  if (!item.open) return <Resolved item={item} />;

  const next = chosen ?? defaultContinueMode(offered, mode);
  const reply = (answer: PlanAnswer) => {
    setSent(true);
    setError(null);
    replyPlan(sessionId, item.request.requestId, answer).catch(
      (err: unknown) => {
        setSent(false);
        setError(errorCode(err));
      },
    );
  };
  const options: PickerOption<PermissionMode>[] = offered.map((m) => ({
    value: m,
    label: t(modeNames[m], { ns: "composer" }),
    warn: m === "full_access",
  }));

  return (
    <div className="card plan" data-testid={item.key}>
      <div className="card-h">
        <Icon name="map" className="file-ic" />
        <div className="grow">
          <div className="card-title">{t("plan")}</div>
        </div>
        <span className="label-pill pill-sun">{t("awaitingApproval")}</span>
      </div>
      <div className="card-b answer plan-body">
        <Markdown text={item.request.plan} />
      </div>
      {error && (
        <p className="card-error q-error" role="alert">
          {t(error, { ns: "errors" })}
        </p>
      )}
      {rejecting ? (
        <div className="card-foot plan-feedback">
          <textarea
            className="plan-fb"
            rows={2}
            aria-label={t("planFeedback")}
            placeholder={t("planFeedback")}
            value={feedback}
            readOnly={sent}
            autoFocus
            onChange={(e) => setFeedback(e.target.value)}
          />
          <div className="row-inline">
            <button
              className="btn primary"
              type="button"
              disabled={sent}
              onClick={() => {
                const text = feedback.trim();
                reply({
                  decision: "rejected",
                  ...(text === "" ? {} : { feedback: text }),
                });
              }}
            >
              {t("sendFeedback")}
            </button>
            <button
              className="btn ghost"
              type="button"
              disabled={sent}
              onClick={() => setRejecting(false)}
            >
              {t("cancel")}
            </button>
          </div>
        </div>
      ) : (
        <div className="card-foot">
          <button
            className="btn primary"
            type="button"
            disabled={sent}
            onClick={() =>
              reply(
                next
                  ? { decision: "approved", mode: next }
                  : { decision: "approved" },
              )
            }
          >
            {t("approvePlan")}
          </button>
          {next && options.length > 1 && (
            <Picker
              label={t("continueIn", {
                mode: t(modeNames[next], { ns: "composer" }),
              })}
              heading={t("continueInLabel")}
              value={next}
              options={options}
              onChange={setChosen}
              disabled={sent}
            >
              <Icon name="shield" />
              <span>{t(modeNames[next], { ns: "composer" })}</span>
            </Picker>
          )}
          <button
            className="btn"
            type="button"
            disabled={sent}
            onClick={() => setRejecting(true)}
          >
            {t("keepPlanning")}
          </button>
        </div>
      )}
    </div>
  );
}
