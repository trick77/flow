import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber } from "../i18n/format";
import { Icon } from "../shell";
import { copyText } from "./clipboard";
import { countdown, safeUrl, type ActiveFlow } from "./github";
import type { FlowState } from "./useDeviceFlow";

const two = (n: number) => formatNumber(n, { minimumIntegerDigits: 2 });

// flowEnded: the flow stopped without a connection (or with a failure the
// owner can try again after).
export function flowEnded(flow: FlowState): boolean {
  return (
    flow.phase === "expired" ||
    flow.phase === "denied" ||
    flow.phase === "ended" ||
    flow.phase === "failed"
  );
}

// DeviceOutcome is the line under a device flow that has ended: connected,
// ended elsewhere, or why it failed. Nothing while it runs or before.
export function DeviceOutcome({ flow }: { flow: FlowState }) {
  const { t } = useTranslation("settings");
  if (flow.phase === "connected") {
    return (
      <p className="form-status ok conn-note" role="status">
        {flow.connection.login
          ? t("deviceConnectedAs", { login: flow.connection.login })
          : t("deviceConnected")}
      </p>
    );
  }
  if (flow.phase === "ended") {
    return (
      <p className="form-status conn-note" role="status">
        {t("deviceEnded")}
      </p>
    );
  }
  if (!flowEnded(flow)) return null;
  return (
    <p className="page-error" role="alert">
      {flow.phase === "expired"
        ? t("deviceExpired")
        : flow.phase === "denied"
          ? t("deviceDenied")
          : flow.phase === "failed"
            ? `${t("deviceFailed")} ${t(flow.code, { ns: "errors" })}`
            : null}
    </p>
  );
}

// DevicePanel shows the code to enter on GitHub: large, with a copy button
// and a link to GitHub's page, the time left and whether the page still
// waits. Settings and the project form show the same one.
export function DevicePanel({
  flow,
  remainingMs,
  paused,
  onCancel,
}: {
  flow: ActiveFlow;
  remainingMs: number;
  paused: boolean;
  onCancel: () => void;
}) {
  const { t } = useTranslation("settings");
  const [copied, setCopied] = useState<"ok" | "failed" | null>(null);
  const codeRef = useRef<HTMLOutputElement>(null);
  const clear = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(clear.current), []);
  const url = safeUrl(flow.verificationUri);
  const left = countdown(remainingMs);

  const copy = () => {
    void copyText(flow.userCode).then((ok) => {
      clearTimeout(clear.current);
      setCopied(ok ? "ok" : "failed");
      if (ok) {
        clear.current = setTimeout(() => setCopied(null), 2000);
      } else if (codeRef.current) {
        // Select the code: the owner copies it by hand.
        const range = document.createRange();
        range.selectNodeContents(codeRef.current);
        const sel = window.getSelection();
        sel?.removeAllRanges();
        sel?.addRange(range);
      }
    });
  };

  return (
    <div className="conn-device" role="region" aria-label={t("deviceTitle")}>
      <p className="device-lead">{t("deviceEnter")}</p>
      <div className="device-code-row">
        <output
          ref={codeRef}
          className="device-code"
          aria-label={t("deviceCode")}
        >
          {flow.userCode}
        </output>
        <div className="row-inline">
          <button type="button" className="btn sm" onClick={copy}>
            <Icon name={copied === "ok" ? "check" : "file"} />
            {copied === "ok" ? t("deviceCopied") : t("deviceCopy")}
          </button>
          {url && (
            <a
              className="btn sm primary"
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              title={t("deviceOpenHint")}
            >
              <Icon name="external" />
              {t("deviceOpen")}
            </a>
          )}
        </div>
      </div>
      {copied === "failed" && (
        <p className="form-status" role="alert">
          {t("deviceCopyFailed")}
        </p>
      )}
      {copied === "ok" && (
        <span className="sr" role="status">
          {t("deviceCopied")}
        </span>
      )}
      <p className="device-status">
        <span role="status">
          {paused ? t("devicePaused") : t("deviceWaiting")}
        </span>{" "}
        <span className="device-left">
          {t("deviceExpiresIn", {
            time: t("deviceTime", {
              minutes: formatNumber(left.minutes),
              seconds: two(left.seconds),
            }),
          })}
        </span>
      </p>
      <div className="row-inline">
        <button type="button" className="btn sm ghost" onClick={onCancel}>
          {t("cancel")}
        </button>
      </div>
    </div>
  );
}
