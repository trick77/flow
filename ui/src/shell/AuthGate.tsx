import {
  createContext,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import {
  errorCode,
  getAuthSession,
  LOGIN_PATH,
  logout,
  onStreamGaveUp,
  onUnauthenticated,
} from "../api";
import type { ErrorCode } from "../api/types.gen";

// Account is the signed-in owner as the shell shows it.
export interface Account {
  name: string;
  signOut: () => void;
}

// AccountContext carries the account below AuthGate; null outside it.
export const AccountContext = createContext<Account | null>(null);

type State =
  | { kind: "loading" }
  | { kind: "signedOut"; error?: ErrorCode; failed?: boolean }
  | { kind: "signedIn"; name: string };

// signInFailed: the page was opened with the marker the server's callback
// sends the browser back with when a sign-in was refused (backend
// auth.SignInFailed, "/?signin=failed").
function signInFailed(): boolean {
  return new URLSearchParams(window.location.search).get("signin") === "failed";
}

// AuthGate renders children only for a signed-in owner: the session endpoint
// decides first, then any API call answering unauthenticated (an expired or
// cleared session) swaps back to the sign-in screen, and so does a stream the
// browser gave up on when the session endpoint then says signed out.
export function AuthGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<State>({ kind: "loading" });
  // Read once, at mount (a pure read: Strict Mode runs effects twice).
  const [failed] = useState(signInFailed);

  useEffect(() => {
    let live = true;
    if (failed) {
      // Shown once: a reload or a later sign-out does not repeat it.
      window.history.replaceState(null, "", window.location.pathname);
    }
    getAuthSession()
      .then(
        (s): State =>
          s.signedIn
            ? { kind: "signedIn", name: s.name ?? "" }
            : { kind: "signedOut", failed },
        (err: unknown): State => ({
          kind: "signedOut",
          error: errorCode(err),
          failed,
        }),
      )
      .then((next) => {
        if (live) setState(next);
      });
    const offUnauthenticated = onUnauthenticated(() =>
      setState({ kind: "signedOut" }),
    );
    // A stream that ended does not say why; only a definite "signed out"
    // swaps (a network blip must not).
    const offGaveUp = onStreamGaveUp(() => {
      getAuthSession().then(
        (s) => {
          if (live && !s.signedIn) setState({ kind: "signedOut" });
        },
        () => {},
      );
    });
    return () => {
      live = false;
      offUnauthenticated();
      offGaveUp();
    };
  }, [failed]);

  // A failed sign-out leaves the session as it was: the menu stays usable for
  // another try (an expired one already swapped through onUnauthenticated).
  const signOut = useCallback(() => {
    logout().then(
      () => setState({ kind: "signedOut" }),
      () => {},
    );
  }, []);

  const name = state.kind === "signedIn" ? state.name : null;
  const account = useMemo(
    () => (name === null ? null : { name, signOut }),
    [name, signOut],
  );

  if (state.kind === "loading") return null;
  if (state.kind === "signedOut") {
    return <SignIn error={state.error} failed={state.failed} />;
  }
  return (
    <AccountContext.Provider value={account}>
      {children}
    </AccountContext.Provider>
  );
}

// SignIn is the whole page while signed out: the wordmark and one way in. The
// login endpoint is a navigation (it redirects to the identity provider).
// failed: the last sign-in was refused; why is only in the server log.
export function SignIn({
  error,
  failed,
}: {
  error?: ErrorCode;
  failed?: boolean;
}) {
  const { t } = useTranslation("shell");
  return (
    <main className="grid h-full place-items-center bg-bg px-4">
      <div className="flex flex-col items-center gap-5 text-center">
        <h1 className="font-serif text-[2.5rem] leading-[3rem] font-medium text-ink-bright">
          {t("appName", { ns: "common" })}
        </h1>
        <p className="text-sm text-muted">{t("signInHint")}</p>
        {failed && (
          <p className="text-sm text-danger" role="alert">
            {t("signInFailed")}
          </p>
        )}
        {error && (
          <p className="text-sm text-danger" role="alert">
            {t(error, { ns: "errors" })}
          </p>
        )}
        <a
          className="rounded-ui bg-accent px-5 py-2 text-sm font-medium text-ink-bright transition-colors hover:bg-accent-strong"
          href={LOGIN_PATH}
        >
          {t("signIn")}
        </a>
      </div>
    </main>
  );
}
