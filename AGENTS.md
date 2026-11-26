# flow

Self-hosted: runs Claude Code and OpenCode sessions in sandboxes on a Linux server, operated from the browser.

## Components
- `backend/cmd/flow`: server. JSON API, later auth, projects, sessions; serves the embedded UI.
- `backend/cmd/flowd`: supervisor inside each sandbox's Linux system container, owns the agent processes. Same
  module as the server; the image carries it for linux/amd64 + arm64, the server pushes the matching one in.
- `ui/`: React SPA, built into `backend/web/dist`, embedded in the server binary.
- `sidecar/`: Node package, will host the Claude Agent SDK and translate its messages into Flow's event stream.
  Not in Dependabot: its deps are bumped by hand.

## Working conventions
- Commit subjects + PR titles: Go style `area: what` (`server: …`, `flowd: …`, `ui: …`, `sidecar: …`), gated by ship; release notes list them by area.
- Fresh clone: `ship hooks` before the first push (check, conform and init install it too); without the pre-push hook a push skips the gate.
- Env vars: `BACKEND_*`, never the project name.
- UI strings only through i18next, one namespace per UI area (`ui/src/i18n/locales/en/<area>.ts`), English only.
  Locale `en-CH`: numbers, dates, times via `ui/src/i18n/format.ts` (1'234.5, 26.11.2026, 14:05).
- No README on purpose.
