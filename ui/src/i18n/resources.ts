import { common } from "./locales/en/common";
import { shell } from "./locales/en/shell";

// One namespace per UI area. A new area gets its own file under locales/en and
// an entry here; components name the namespace they read.
export const resources = {
  en: { common, shell },
} as const;

export const namespaces = ["common", "shell"] as const;

export const defaultNS = "common";
