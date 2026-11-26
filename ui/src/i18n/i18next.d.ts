import "i18next";

import type { defaultNS, resources } from "./resources";

// Typed keys: t("missing") is a compile error, not raw key text on screen.
declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: typeof defaultNS;
    resources: (typeof resources)["en"];
  }
}
