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
import signInImage from "./sign-in.webp";

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
// The background is decoration: a CSS image (no broken-image icon when it
// fails, bg-bg stays). Landscape enlarges it from the top left, 125%, 170% on
// short screens, so the dark, empty corner of the picture lies behind the
// centred block and the bright code lines sit lower right (at 125% a short
// landscape page put the error line on them, 3.7:1). The soft black ellipse
// behind the block holds text-muted and text-danger at 4.5:1 with or without
// the image: danger on bare bg-bg is 4.16:1 and needs at least 25% black
// there (rgb 23); the core is 35%.
export function SignIn({
  error,
  failed,
}: {
  error?: ErrorCode;
  failed?: boolean;
}) {
  const { t } = useTranslation("shell");
  return (
    <main className="relative isolate grid h-full place-items-center overflow-hidden bg-bg px-4">
      <div
        className="absolute top-0 left-0 -z-10 h-full w-full bg-cover bg-left-top landscape:h-[125%] landscape:w-[125%] [@media(orientation:landscape)_and_(max-height:600px)]:h-[170%] [@media(orientation:landscape)_and_(max-height:600px)]:w-[170%]"
        style={{ backgroundImage: `url(${signInImage})` }}
        aria-hidden="true"
        data-testid="sign-in-image"
      />
      <div
        className="absolute inset-0 -z-10 bg-[radial-gradient(ellipse_600px_420px_at_center,rgb(0_0_0/0.35)_36%,rgb(0_0_0/0.27)_50%,rgb(0_0_0/0.16)_64%,rgb(0_0_0/0.07)_79%,rgb(0_0_0/0.02)_91%,transparent)]"
        aria-hidden="true"
      />
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
