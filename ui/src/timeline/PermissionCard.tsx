import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, replyPermission } from "../api";
import type { ErrorCode, ReplyDecision } from "../api/types.gen";
import {
  PERMISSION_DECISION_VALUES,
  PERMISSION_KIND_VALUES,
  type PermissionDecision,
  type PermissionKind,
  type PermissionMode,
  type Provider,
} from "../contract/events.gen";
import { Icon } from "../shell/Icon";
import { modeNames as modeKeys, useAgentName } from "./agentName";
import type { PermissionItem } from "./fold";

// Resolved is the compact row a request leaves once decided.
function Resolved({ item }: { item: PermissionItem }) {
  const { t } = useTranslation("timeline");
  const d = item.decision as PermissionDecision;
  const known = PERMISSION_DECISION_VALUES.includes(d) ? d : "expired";
  const allowed = known === "allow_once" || known === "allow_always";
  const r = item.request;
  return (
    <div className={allowed ? "resolved" : "resolved denied"}>
      <Icon name={allowed ? "check" : "x"} />
      <span className="what">
        {allowed && item.remembered === true
          ? t("resolvedRemembered")
          : t(`resolved.${known}`)}{" "}
        <span className="mono">{r.command ?? r.path ?? r.tool}</span>
      </span>
    </div>
  );
}

// PermissionCard asks the owner about one tool call. The buttons stay
// disabled after an answer until permission.resolved arrives in the stream.
export function PermissionCard({
  item,
  sessionId,
  provider,
}: {
  item: PermissionItem;
  sessionId: string;
  provider?: Provider;
}) {
  const { t } = useTranslation("timeline");
  const agent = useAgentName(provider);
  const [sent, setSent] = useState<ReplyDecision | null>(null);
  const [error, setError] = useState<ErrorCode | null>(null);
  if (item.decision !== undefined) return <Resolved item={item} />;

  const r = item.request;
  const kind: PermissionKind = PERMISSION_KIND_VALUES.includes(r.kind)
    ? r.kind
    : "other";
  const mode = modeKeys[r.mode] as
    (typeof modeKeys)[PermissionMode] | undefined;

  const answer = (decision: ReplyDecision) => {
    setSent(decision);
    setError(null);
    replyPermission(sessionId, r.requestId, decision).catch((err: unknown) => {
      setSent(null);
      setError(errorCode(err));
    });
  };

  const body = r.command ?? r.path;
  // Without a rule from the provider, "Allow for this session" is Flow's own:
  // the host remembers this exact request (docs/events.md), which the hint says.
  const exact =
    kind === "command"
      ? "command"
      : kind === "edit" || kind === "write"
        ? "file"
        : "request";
  return (
    <div className="card perm" data-testid={item.key}>
      <div className="card-h">
        <Icon name="shield" className="file-ic" />
        <div className="grow">
          <div className="card-title">
            {t(`permissionTitle.${kind}`, { agent, tool: r.tool })}
          </div>
        </div>
        <span className="label-pill pill-sun">{t("approvalNeeded")}</span>
      </div>
      <div className="card-b">
        {body !== undefined && (
          <pre className="cmd cmd-wrap">
            {r.command !== undefined && (
              <span className="pr">{t("commandPrompt")}</span>
            )}
            {body}
          </pre>
        )}
        {(r.cwd !== undefined || mode !== undefined) && (
          <div className="cwd">
            {r.cwd !== undefined && (
              <>
                {t("cwdIn")} <span className="mono">{r.cwd}</span>
              </>
            )}
            {r.cwd !== undefined && mode !== undefined && t("separator")}
            {mode !== undefined &&
              t("modeIs", { mode: t(mode, { ns: "composer" }) })}
          </div>
        )}
        {!r.canAlwaysAllow && (
          <p className="card-sub perm-hint">{t(`allowExact.${exact}`)}</p>
        )}
        {error && (
          <p className="card-error" role="alert">
            {t(error, { ns: "errors" })}
          </p>
        )}
      </div>
      <div className="card-foot">
        <button
          className="btn primary"
          type="button"
          disabled={sent !== null}
          onClick={() => answer("allow_once")}
        >
          {t("allowOnce")}
        </button>
        <button
          className="btn"
          type="button"
          disabled={sent !== null}
          onClick={() => answer("allow_always")}
        >
          {t("allowAlways")}
        </button>
        <button
          className="btn ghost"
          type="button"
          disabled={sent !== null}
          onClick={() => answer("deny")}
        >
          {t("deny")}
        </button>
      </div>
    </div>
  );
}
