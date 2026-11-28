import { useState } from "react";
import { useTranslation } from "react-i18next";

import { disconnectGitHub, errorCode } from "../api";
import type { ErrorCode, GitHubConnection } from "../api/types.gen";
import { formatDate } from "../i18n/format";
import { Icon } from "../shell";
import { DeviceOutcome, DevicePanel, flowEnded } from "./DevicePanel";
import { rowState, type RowState } from "./github";
import { useDeviceFlow, type FlowState } from "./useDeviceFlow";

const pill: Record<
  RowState,
  {
    key:
      | "pillUnavailable"
      | "pillNotConnected"
      | "pillConnecting"
      | "pillConnected"
      | "pillReconnect"
      | "pillError";
    tone: string;
  }
> = {
  unavailable: { key: "pillUnavailable", tone: "pill-mute" },
  disconnected: { key: "pillNotConnected", tone: "pill-mute" },
  connecting: { key: "pillConnecting", tone: "pill-sun" },
  connected: { key: "pillConnected", tone: "pill-ok" },
  reconnect: { key: "pillReconnect", tone: "pill-sun" },
  error: { key: "pillError", tone: "pill-err" },
};

// shownState is the row's state: the server's, unless this page knows
// better (a code is out, or the server turned out to have no GitHub App).
function shownState(conn: GitHubConnection, flow: FlowState): RowState {
  if (flow.phase === "unavailable") return "unavailable";
  if (flow.phase === "waiting" || flow.phase === "starting") {
    return "connecting";
  }
  return rowState(conn);
}

// GitHubRow is the GitHub connection in Settings: its state, connecting with
// GitHub's device flow (a code the owner enters on GitHub, polled until
// confirmed) and disconnecting, which asks first. onConnected gets the
// connection a confirmed code answered; onChanged asks for the connections
// again after a disconnect.
export function GitHubRow({
  connection,
  onConnected,
  onChanged,
}: {
  connection: GitHubConnection;
  onConnected: (c: GitHubConnection) => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation("settings");
  const device = useDeviceFlow(onConnected);
  const flow = device.state;
  const state = shownState(connection, flow);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<ErrorCode>();

  const remove = () => {
    setBusy(true);
    setFailure(undefined);
    disconnectGitHub().then(
      () => {
        setConfirming(false);
        setBusy(false);
        device.reset();
        onChanged();
      },
      (err: unknown) => {
        setFailure(errorCode(err));
        setConfirming(false);
        setBusy(false);
      },
    );
  };
  const connect = () => {
    setFailure(undefined);
    device.start();
  };

  const sep = t("githubSeparator");
  let description: string;
  switch (state) {
    case "unavailable":
      description = t("githubUnavailable");
      break;
    case "disconnected":
      description = t("githubDisconnectedHint");
      break;
    case "connecting":
      // With a code out here, the panel below says what to do.
      description =
        flow.phase === "waiting" || flow.phase === "starting"
          ? t("githubDisconnectedHint")
          : t("githubPendingHint");
      break;
    case "connected": {
      const parts = [
        connection.login
          ? t("githubConnectedAs", { login: connection.login })
          : t("githubConnected"),
      ];
      if (connection.connectedAt) {
        parts.push(
          t("githubSince", {
            date: formatDate(new Date(connection.connectedAt)),
          }),
        );
      }
      if (connection.refreshExpiresAt) {
        parts.push(
          t("githubRenewBy", {
            date: formatDate(new Date(connection.refreshExpiresAt)),
          }),
        );
      }
      description = parts.join(sep);
      break;
    }
    case "reconnect":
      description = t("githubExpiredHint");
      break;
    default:
      description = t("githubUnknownState");
  }

  const ended = flowEnded(flow);
  const waiting = flow.phase === "waiting" ? flow.flow : null;
  const starting = flow.phase === "starting";
  const showActs = !confirming && !waiting && state !== "unavailable";
  const p = pill[state];

  return (
    <div className="conn-row" aria-label={t("github")} role="group">
      <div className="conn-main">
        <span className="ic">
          <Icon name="repo" />
        </span>
        <div className="grow">
          <div className="nm">{t("github")}</div>
          <div className={state === "reconnect" ? "ds warn" : "ds"}>
            {description}
          </div>
        </div>
        <span className={`label-pill ${p.tone}`} role="status">
          {t(p.key)}
        </span>
        {showActs && (
          <div className="acts">
            {state !== "connected" && (
              <button
                type="button"
                className={state === "reconnect" ? "btn sm primary" : "btn sm"}
                disabled={starting || busy}
                onClick={connect}
              >
                {starting
                  ? t("starting")
                  : ended
                    ? t("tryAgain")
                    : state === "connecting"
                      ? t("startAgain")
                      : state === "disconnected"
                        ? t("connect")
                        : t("reconnect")}
              </button>
            )}
            {(state === "connected" ||
              state === "reconnect" ||
              state === "error") && (
              <button
                type="button"
                className="btn sm ghost"
                disabled={busy || starting}
                onClick={() => {
                  setFailure(undefined);
                  setConfirming(true);
                }}
              >
                {t("disconnect")}
              </button>
            )}
          </div>
        )}
      </div>
      {waiting && (
        <DevicePanel
          flow={waiting}
          remainingMs={device.remainingMs}
          paused={device.paused}
          onCancel={device.cancel}
        />
      )}
      <DeviceOutcome flow={flow} />
      {confirming && (
        <div className="confirm" role="alertdialog" aria-label={t("github")}>
          {t("confirmDisconnect")}
          <div className="row-inline">
            <button
              type="button"
              className="btn sm danger"
              disabled={busy}
              onClick={remove}
            >
              {busy ? t("disconnecting") : t("disconnect")}
            </button>
            <button
              type="button"
              className="btn sm ghost"
              onClick={() => setConfirming(false)}
            >
              {t("cancel")}
            </button>
          </div>
        </div>
      )}
      {failure && (
        <p className="page-error" role="alert">
          {t(failure, { ns: "errors" })}
        </p>
      )}
    </div>
  );
}
