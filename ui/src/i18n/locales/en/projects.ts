// The Projects page. Owned by the projects and settings pages area.
export const projects = {
  title: "Projects",
  lead: "A project is one or more GitHub repositories in one warm sandbox. Sessions run in worktrees inside it.",
  add: "Add project",
  empty: "No projects yet.",
  emptyHint: "Add one with the repositories its sessions will work in.",
  loadFailed: "The projects could not be loaded.",
  // The create form.
  createTitle: "Add project",
  close: "Close",
  name: "Name",
  // The name follows the first repository until the owner types one.
  namePlaceholder: "Named after the first repository",
  nameHint: "Shown in the sidebar and on the project's card.",
  repos: "Repositories",
  create: "Create project",
  creating: "Creating…",
  needRepos: "Choose at least one repository.",
  needName: "Give the project a name.",
  // The repository picker (GitHub connected).
  pickRepos: "Choose repositories",
  pickMore: "Add a repository",
  repoSearch: "Search repositories",
  repoSearchPlaceholder: "Search your repositories",
  repoList: "Repositories on GitHub",
  repoLoading: "Loading your repositories…",
  repoSearching: "Searching…",
  repoNoMatch: "No repository matches “{{query}}”.",
  repoNone: "This GitHub connection reaches no repositories.",
  repoShown: "Showing the first {{shown}} of {{total}}. Type to narrow.",
  repoCapped: "GitHub listed the first {{n}}. Type to search all of them.",
  repoCappedMatches: "Showing the first {{n}} matches. Type more to narrow.",
  // A repository the server refuses in a project (its directory rule).
  repoBadDir:
    "Cannot be added: a name starting with a dot, dash or underscore, or longer than 63 characters, cannot be a directory in the sandbox.",
  repoSameDir:
    "Cannot be added next to {{name}}: both would get the same directory.",
  repoListFailed: "The repositories could not be loaded.",
  repoRetry: "Try again",
  repoAddByName: "Add {{name}}",
  // The lock icon's name; public repositories carry no mark.
  repoNotPublic: "not public",
  repoRemove: "Remove {{name}}",
  repoChosen: "Chosen repositories",
  repoMatches_one: "{{n}} repository",
  repoMatches_other: "{{n}} repositories",
  // GitHub not connected (but available) in the form.
  ghOffer:
    "Connect GitHub to choose from your repositories, the ones that are not public included.",
  ghExpired:
    "The GitHub connection has expired. Connect again to choose from your repositories.",
  ghConnect: "Connect GitHub",
  ghConnectAgain: "Connect again",
  ghStarting: "Starting…",
  ghTryAgain: "Try again",
  // The server has no GitHub client id.
  ghUnavailable:
    "Only public repositories work on this installation. The operator enables GitHub with BACKEND_GITHUB_CLIENT_ID.",
  // Adding a public repository by name.
  byName: "Add a public repository by name",
  byNameOr: "Or add a public repository by name",
  byNamePlaceholder: "cli/cli",
  byNameAdd: "Add",
  byNameInvalid: "Use owner/name or a github.com address.",
  byNameTwice: "This repository is chosen already.",
  // Field-level refusals of the create form (invalid_request with a field).
  invalidName: "Give the project a name of at most 64 characters.",
  nameTaken: "A project with this name exists already.",
  invalidRepos:
    "A repository was refused: GitHub does not know it by that name, or this connection cannot reach it.",
  // github_not_connected on a repository: not found as a public one.
  notFoundPublic:
    "A repository was not found among the public ones. If it is not public, connect GitHub first.",
  // Cards.
  repoCount_one: "{{n}} repository",
  repoCount_other: "{{n}} repositories",
  reposIn: "Repositories in {{name}}",
  created: "Created",
  lastUsed: "Last used",
  flowd: "flowd",
  never: "Never",
  sandboxNone: "Not set up",
  sandboxSetup: "Setting up",
  sandboxWarm: "Warm",
  sandboxStopping: "Stopping",
  sandboxStopped: "Stopped",
  sandboxFailed: "Failed",
  sandboxDeleting: "Deleting",
  // An operation runs on the sandbox now (busy).
  busySetup: "Setting up…",
  busyStopping: "Stopping…",
  busyDeleting: "Deleting…",
  busyWorking: "Working…",
  busyNote: "Start, stop, rebuild and delete return once this is done.",
  // setup or stopping with nothing running: the operation did not finish.
  staleSetup: "Setup did not finish",
  staleStop: "Stop did not finish",
  start: "Start sandbox",
  stop: "Stop sandbox",
  rebuild: "Rebuild",
  // The log of the last setup run, offered when a setup script failed.
  setupLog: "Setup log",
  hideSetupLog: "Hide setup log",
  setupLogLoading: "Reading the setup log…",
  setupLogFailed: "The setup log could not be read.",
  setupLogEmpty: "The setup log is empty.",
  // The log is read from its end; earlier lines on demand.
  setupLogTruncated_one: "Truncated — showing the last {{n}} line.",
  setupLogTruncated_other: "Truncated — showing the last {{n}} lines.",
  setupLogEarlier: "Load earlier lines",
  setupLogEarlierLoading: "Loading earlier lines…",
  delete: "Delete",
  // Confirmations: what goes, in plain words.
  confirmRebuild:
    "Rebuild the sandbox of {{name}} from the base image? Everything on its disk goes: checkouts are cloned again, archived sessions' logs and kept worktrees are lost.",
  confirmRebuildAction: "Rebuild sandbox",
  confirmDelete:
    "Delete {{name}}? Its sandbox, the archived sessions and everything on the sandbox's disk go. Repositories on GitHub are not touched.",
  confirmDeleteAction: "Delete project",
  cancel: "Cancel",
  // project_has_sessions with the count the app knows.
  openSessions_one:
    "1 session of this project is still open. Archive it first (in its header).",
  openSessions_other:
    "{{n}} sessions of this project are still open. Archive them first (in their headers).",
};
