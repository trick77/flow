import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorCode, updateSession } from "../api";
import type {
  AgentCatalog,
  ErrorCode,
  PermissionMode,
  Provider,
  SessionUpdate,
} from "../api/types.gen";
import { AgentPill, useAgentName } from "../shell/AgentPill";
import { Icon } from "../shell/Icon";
import { aboveCap, modelLabel } from "./catalog";
import { modeKeys } from "./modes";
import { Picker, type PickerOption } from "./Picker";

// The fields the pickers change, as the answer gave them back.
export type SettingsPatch = Pick<SessionUpdate, "model" | "mode">;

// SessionPickers are a running session's model and permission mode in the
// composer bar. A pick shows at once and is sent as updateSession; the
// answer's values go to onUpdated, a refusal puts the old value back and
// goes to onError. Only the session's own agent's models are offered (a
// session cannot change agent); modes above the cap are disabled. The model
// changes between turns (docs/events.md, "Model per turn"): picked while one
// runs, it applies to the next, and the menu says so. Without a catalog they
// are a read-only display.
export function SessionPickers({
  sessionId,
  provider,
  model,
  mode,
  modeCap,
  catalog,
  turnRunning,
  onUpdated,
  onError,
}: {
  sessionId: string;
  provider: Provider;
  model: string;
  mode: PermissionMode;
  modeCap?: PermissionMode;
  catalog?: AgentCatalog;
  turnRunning: boolean;
  onUpdated?: (fields: SettingsPatch) => void;
  onError: (code: ErrorCode | null) => void;
}) {
  const { t } = useTranslation("composer");
  const agentName = useAgentName();
  // The pick under way, shown until the answer.
  const [pending, setPending] = useState<SettingsPatch>({});
  const shownModel = pending.model ?? model;
  const shownMode = pending.mode ?? mode;
  const label = modelLabel(catalog, provider, shownModel);

  const change = (update: SettingsPatch) => {
    setPending((p) => ({ ...p, ...update }));
    onError(null);
    updateSession(sessionId, update).then(
      (s) => {
        setPending({});
        onUpdated?.({
          ...(update.model !== undefined ? { model: s.summary.model } : {}),
          ...(update.mode !== undefined ? { mode: s.summary.mode } : {}),
        });
      },
      (err: unknown) => {
        setPending({});
        onError(errorCode(err));
      },
    );
  };

  const busy = pending.model !== undefined || pending.mode !== undefined;
  const providerInfo = catalog?.providers.find((p) => p.provider === provider);
  const modelOptions: PickerOption<string>[] = (providerInfo?.models ?? []).map(
    (m) => ({ value: m.id, label: m.label, description: m.via }),
  );
  const modes = (catalog?.modes ?? []).filter((m) => m in modeKeys);
  const capped = modes.some((m) => aboveCap(modes, m, modeCap));
  const modeOptions: PickerOption<PermissionMode>[] = modes.map((m) => ({
    value: m,
    label: t(modeKeys[m].label),
    description: t(modeKeys[m].hint),
    warn: m === "full_access",
    disabled: aboveCap(modes, m, modeCap),
  }));

  const modelContent = (
    <>
      <AgentPill provider={provider} />
      <span>
        <span className="long">
          {t("agentPrefix", { agent: agentName(provider) })}
        </span>
        {label}
      </span>
    </>
  );
  const modelName = t("modelOf", { agent: agentName(provider), model: label });
  const known = shownMode in modeKeys;

  return (
    <>
      {modelOptions.length > 0 ? (
        <Picker
          label={modelName}
          heading={agentName(provider)}
          value={shownModel}
          options={modelOptions}
          onChange={(m) => m !== shownModel && change({ model: m })}
          disabled={busy}
          note={turnRunning ? t("modelNextTurn") : undefined}
          title={turnRunning ? t("modelNextTurn") : undefined}
        >
          {modelContent}
        </Picker>
      ) : (
        <span className="picker picker-ro" role="img" aria-label={modelName}>
          <span aria-hidden="true">{modelContent}</span>
        </span>
      )}
      {known &&
        (modeOptions.length > 0 ? (
          <Picker
            label={t("modeOf", { mode: t(modeKeys[shownMode].label) })}
            heading={t("mode")}
            note={capped ? t("modeCapped") : undefined}
            value={shownMode}
            options={modeOptions}
            onChange={(m) => m !== shownMode && change({ mode: m })}
            disabled={busy}
          >
            <Icon name="shield" />
            <span>{t(modeKeys[shownMode].label)}</span>
          </Picker>
        ) : (
          <span
            className="picker picker-ro"
            role="img"
            aria-label={t("modeOf", { mode: t(modeKeys[shownMode].label) })}
          >
            <Icon name="shield" />
            <span aria-hidden="true">{t(modeKeys[shownMode].label)}</span>
          </span>
        ))}
    </>
  );
}
