import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import {
  ApiError,
  createApiKey,
  deleteApiKey,
  errorCode,
  getConnections,
  getProfile,
  updateProfile,
} from "../api";
import type {
  ApiKey,
  ErrorCode,
  GitHubConnection,
  Profile,
} from "../api/types.gen";
import { formatDateTime, formatNumber } from "../i18n/format";
import { Icon, type IconName } from "../shell";
import { GitHubRow } from "./GitHubRow";
import { Page } from "./Page";

// MAX_SECRET is the backend's bound on a stored secret (credentials.MaxSecret).
const MAX_SECRET = 4096;

type Loaded =
  | { kind: "loading" }
  | { kind: "failed"; code: ErrorCode }
  | {
      kind: "ready";
      profile: Profile;
      keys: ApiKey[];
      github: GitHubConnection;
    };

// SettingsPage is the owner's profile (the git identity sessions commit with)
// and the connections: GitHub, and the Claude credentials (the subscription
// token and the API key, shown only as stored or not, never as their values).
// connectionsRev counts the app stream's connections.updated frames: each
// one reads the connections again.
export function SettingsPage({
  nav,
  onBack,
  connectionsRev = 0,
}: {
  nav: ReactNode;
  onBack?: () => void;
  connectionsRev?: number;
}) {
  const { t } = useTranslation("settings");
  const [loaded, setLoaded] = useState<Loaded>({ kind: "loading" });
  // Bumped to read the connections again after an action of this page.
  const [refetch, setRefetch] = useState(0);

  useEffect(() => {
    let live = true;
    Promise.all([getProfile(), getConnections()]).then(
      ([profile, connections]) => {
        if (live) {
          setLoaded({
            kind: "ready",
            profile,
            keys: connections.apiKeys,
            github: connections.github,
          });
        }
      },
      (err: unknown) => {
        if (live) setLoaded({ kind: "failed", code: errorCode(err) });
      },
    );
    return () => {
      live = false;
    };
  }, []);

  // A later read changes only the connections; a failed one keeps what is
  // shown (the stream or the next action reads again).
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    let live = true;
    getConnections().then(
      (c) => {
        if (live) {
          setLoaded((l) =>
            l.kind === "ready"
              ? { ...l, keys: c.apiKeys, github: c.github }
              : l,
          );
        }
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [connectionsRev, refetch]);

  const setKeys = (update: (keys: ApiKey[]) => ApiKey[]) =>
    setLoaded((l) => (l.kind === "ready" ? { ...l, keys: update(l.keys) } : l));
  const onConnected = useCallback((github: GitHubConnection) => {
    setLoaded((l) => (l.kind === "ready" ? { ...l, github } : l));
    setRefetch((n) => n + 1);
  }, []);
  const onChanged = useCallback(() => setRefetch((n) => n + 1), []);

  return (
    <Page title={t("title")} lead={t("lead")} nav={nav} onBack={onBack}>
      {loaded.kind === "failed" && (
        <p className="page-error" role="alert">
          {t("loadFailed")} {t(loaded.code, { ns: "errors" })}
        </p>
      )}
      {loaded.kind === "ready" && (
        <>
          <section className="set-sec" aria-labelledby="set-profile">
            <div>
              <h2 id="set-profile">{t("profile")}</h2>
              <p className="sec-hint">{t("profileHint")}</p>
            </div>
            <ProfileForm initial={loaded.profile} />
          </section>
          <section className="set-sec" aria-labelledby="set-connections">
            <div>
              <h2 id="set-connections">{t("connections")}</h2>
              <p className="sec-hint">{t("connectionsHint")}</p>
            </div>
            <div className="conn">
              <GitHubRow
                connection={loaded.github}
                onConnected={onConnected}
                onChanged={onChanged}
              />
              <SecretRow
                kind="claude_token"
                icon="agent"
                stored={loaded.keys.find((k) => k.kind === "claude_token")}
                onChange={setKeys}
              />
              <SecretRow
                kind="anthropic"
                icon="key"
                stored={loaded.keys.find((k) => k.kind === "anthropic")}
                onChange={setKeys}
              />
            </div>
          </section>
        </>
      )}
    </Page>
  );
}

// ProfileForm edits the git identity. It says when only half of it is set:
// sessions then get none.
function ProfileForm({ initial }: { initial: Profile }) {
  const { t } = useTranslation("settings");
  const [saved, setSaved] = useState(initial);
  const [name, setName] = useState(initial.gitName);
  const [email, setEmail] = useState(initial.gitEmail);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [failure, setFailure] = useState<{ field?: string; code: ErrorCode }>();

  const dirty =
    name.trim() !== saved.gitName || email.trim() !== saved.gitEmail;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setDone(false);
    setFailure(undefined);
    updateProfile({ gitName: name, gitEmail: email }).then(
      (p) => {
        setSaved(p);
        setName(p.gitName);
        setEmail(p.gitEmail);
        setBusy(false);
        setDone(true);
      },
      (err: unknown) => {
        setFailure({
          field: err instanceof ApiError ? err.field : undefined,
          code: errorCode(err),
        });
        setBusy(false);
      },
    );
  };
  const half = (saved.gitName === "") !== (saved.gitEmail === "");
  return (
    <form onSubmit={submit} aria-label={t("profile")}>
      <div className="field">
        <label htmlFor="git-name">{t("gitName")}</label>
        <input
          id="git-name"
          className="input"
          type="text"
          autoComplete="off"
          placeholder={t("gitNamePlaceholder")}
          value={name}
          aria-invalid={failure?.field === "gitName" || undefined}
          onChange={(e) => {
            setName(e.target.value);
            setDone(false);
          }}
        />
        {failure?.field === "gitName" && (
          <span className="err" role="alert">
            {t("invalidGitName")}
          </span>
        )}
      </div>
      <div className="field">
        <label htmlFor="git-email">{t("gitEmail")}</label>
        <input
          id="git-email"
          className="input"
          type="text"
          inputMode="email"
          autoComplete="off"
          placeholder={t("gitEmailPlaceholder")}
          value={email}
          aria-invalid={failure?.field === "gitEmail" || undefined}
          onChange={(e) => {
            setEmail(e.target.value);
            setDone(false);
          }}
        />
        {failure?.field === "gitEmail" && (
          <span className="err" role="alert">
            {t("invalidGitEmail")}
          </span>
        )}
      </div>
      {failure &&
        failure.field !== "gitName" &&
        failure.field !== "gitEmail" && (
          <p className="page-error" role="alert">
            {t(failure.code, { ns: "errors" })}
          </p>
        )}
      <div className="row-inline">
        <button type="submit" className="btn" disabled={busy || !dirty}>
          {busy ? t("saving") : t("save")}
        </button>
        {done && !dirty && (
          <span className="form-status ok" role="status">
            {t("saved")}
          </span>
        )}
        {half && !dirty && (
          <span className="form-status" role="status">
            {t("identityHalf")}
          </span>
        )}
      </div>
    </form>
  );
}

type SecretKind = "claude_token" | "anthropic";

const rowText: Record<
  SecretKind,
  {
    name: "claudeToken" | "anthropicKey";
    hint: "claudeTokenHint" | "anthropicKeyHint";
    placeholder: "claudeTokenPlaceholder" | "anthropicKeyPlaceholder";
  }
> = {
  claude_token: {
    name: "claudeToken",
    hint: "claudeTokenHint",
    placeholder: "claudeTokenPlaceholder",
  },
  anthropic: {
    name: "anthropicKey",
    hint: "anthropicKeyHint",
    placeholder: "anthropicKeyPlaceholder",
  },
};

// SecretRow is one stored secret: whether it is stored and since when, a
// field to store or replace it (empty every time it opens: a stored value is
// never shown or sent back), and its delete, which asks first.
function SecretRow({
  kind,
  icon,
  stored,
  onChange,
}: {
  kind: SecretKind;
  icon: IconName;
  stored?: ApiKey;
  onChange: (update: (keys: ApiKey[]) => ApiKey[]) => void;
}) {
  const { t } = useTranslation("settings");
  const text = rowText[kind];
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ field?: string; code: ErrorCode }>();
  const inputId = `secret-${kind}`;

  const close = () => {
    setEditing(false);
    setValue("");
  };
  const store = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    createApiKey({ kind, value }).then(
      (key) => {
        onChange((keys) => [...keys.filter((k) => k.kind !== kind), key]);
        close();
        setBusy(false);
      },
      (err: unknown) => {
        setFailure({
          field: err instanceof ApiError ? err.field : undefined,
          code: errorCode(err),
        });
        setBusy(false);
      },
    );
  };
  const remove = () => {
    if (!stored) return;
    setBusy(true);
    setFailure(undefined);
    deleteApiKey(stored.id).then(
      () => {
        onChange((keys) => keys.filter((k) => k.id !== stored.id));
        setConfirming(false);
        setBusy(false);
      },
      (err: unknown) => {
        setFailure({ code: errorCode(err) });
        setConfirming(false);
        setBusy(false);
      },
    );
  };

  return (
    <div className="conn-row" aria-label={t(text.name)} role="group">
      <div className="conn-main">
        <span className="ic">
          <Icon name={icon} />
        </span>
        <div className="grow">
          <div className="nm">{t(text.name)}</div>
          <div className="ds">
            {stored
              ? t("storedSince", {
                  date: formatDateTime(new Date(stored.createdAt)),
                })
              : t(text.hint)}
          </div>
        </div>
        <span
          className={`label-pill ${stored ? "pill-ok" : "pill-mute"}`}
          role="status"
        >
          {stored ? t("stored") : t("notStored")}
        </span>
        {!editing && !confirming && (
          <div className="acts">
            <button
              type="button"
              className="btn sm"
              disabled={busy}
              onClick={() => {
                setFailure(undefined);
                setEditing(true);
              }}
            >
              {stored ? t("replace") : t("add")}
            </button>
            {stored && (
              <button
                type="button"
                className="btn sm ghost"
                disabled={busy}
                onClick={() => setConfirming(true)}
              >
                {t("remove")}
              </button>
            )}
          </div>
        )}
      </div>
      {editing && (
        <form className="conn-edit" onSubmit={store}>
          <label className="sr" htmlFor={inputId}>
            {t(text.name)}
          </label>
          <div className="row-inline">
            <input
              id={inputId}
              className="input mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder={t(text.placeholder)}
              value={value}
              aria-invalid={failure?.field === "value" || undefined}
              onChange={(e) => setValue(e.target.value)}
              autoFocus
            />
            <button
              type="submit"
              className="btn primary sm"
              disabled={busy || value.trim() === ""}
            >
              {busy ? t("storing") : t("storeAction")}
            </button>
            <button
              type="button"
              className="btn sm ghost"
              onClick={() => {
                setFailure(undefined);
                close();
              }}
            >
              {t("cancel")}
            </button>
          </div>
        </form>
      )}
      {confirming && stored && (
        <div className="confirm" role="alertdialog" aria-label={t(text.name)}>
          {t("confirmRemove", { what: t(text.name) })}
          <div className="row-inline">
            <button
              type="button"
              className="btn sm danger"
              disabled={busy}
              onClick={remove}
            >
              {busy ? t("removing") : t("remove")}
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
          {failure.field === "value"
            ? t("invalidSecret", { max: formatNumber(MAX_SECRET) })
            : t(failure.code, { ns: "errors" })}
        </p>
      )}
    </div>
  );
}
