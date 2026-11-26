import { common } from "./locales/en/common";
import { composer } from "./locales/en/composer";
import { errors } from "./locales/en/errors";
import { panel } from "./locales/en/panel";
import { projects } from "./locales/en/projects";
import { settings } from "./locales/en/settings";
import { shell } from "./locales/en/shell";
import { sidebar } from "./locales/en/sidebar";
import { terminal } from "./locales/en/terminal";
import { timeline } from "./locales/en/timeline";

// One namespace per UI area (docs/ownership.md): an area edits only its own
// file under locales/en. This index is the coordinator's: a new area gets its
// file and an entry here in one change.
export const resources = {
  en: {
    common,
    composer,
    errors,
    panel,
    projects,
    settings,
    shell,
    sidebar,
    terminal,
    timeline,
  },
} as const;

export const namespaces = [
  "common",
  "composer",
  "errors",
  "panel",
  "projects",
  "settings",
  "shell",
  "sidebar",
  "terminal",
  "timeline",
] as const;

export const defaultNS = "common";
