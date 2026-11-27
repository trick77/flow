import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Icon, MenuButton } from "../shell";
import "./pages.css";

// Page is the frame of Projects and Settings: a header with the way back to
// the session (when there is one) and the navigation, then the title, its
// lead and the page's own content in a scroll area that fades at its edges.
export function Page({
  title,
  lead,
  action,
  nav,
  onBack,
  children,
}: {
  title: string;
  lead: string;
  action?: ReactNode;
  nav: ReactNode;
  onBack?: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation("shell");
  return (
    <section className="view" aria-label={title}>
      <header className="hdr">
        <MenuButton />
        {onBack && (
          <button type="button" className="btn sm ghost" onClick={onBack}>
            <Icon name="back" />
            {t("backToSession")}
          </button>
        )}
        <span className="hdr-spacer" />
        {nav}
      </header>
      <div className="pg-body">
        <div className="page">
          <div className="page-in">
            <div className="page-top">
              <div>
                <h1 className="page-title">{title}</h1>
                <p className="page-lead">{lead}</p>
              </div>
              {action}
            </div>
            {children}
          </div>
        </div>
      </div>
    </section>
  );
}
