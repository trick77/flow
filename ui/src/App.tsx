import { useTranslation } from "react-i18next";

// The app shell: the wordmark and, until sessions exist, the empty state.
export function App() {
  const { t } = useTranslation("shell");
  return (
    <div className="flex min-h-screen flex-col bg-bg text-ink">
      <header className="flex items-center border-b border-border px-5 py-3">
        <h1 className="ui-wordmark font-serif font-medium">
          {t("appName", { ns: "common" })}
        </h1>
      </header>
      <main className="flex flex-1 flex-col items-center justify-center gap-1 px-5 text-center">
        <p className="text-ink-soft">{t("empty")}</p>
        <p className="text-sm text-muted">{t("emptyHint")}</p>
      </main>
    </div>
  );
}
