// The Projects page. Owned by the projects and settings pages area.
export const projects = {
  title: "Projects",
  lead: "Each project groups one or more GitHub repositories in one warm sandbox. Sessions run in worktrees inside it.",
  add: "Add project",
  empty: "No projects yet.",
  emptyHint: "Add one with the repositories its sessions will work in.",
  loadFailed: "The projects could not be loaded.",
  // The create form.
  createTitle: "Add project",
  close: "Close",
  name: "Name",
  namePlaceholder: "apps",
  repos: "Repositories",
  reposHint:
    "One per line, as owner/name or https://github.com/owner/name. Only public repositories until GitHub is connected.",
  reposPlaceholder: "owner/name",
  create: "Create project",
  creating: "Creating…",
  // Field-level refusals of the create form (invalid_request with a field).
  invalidName: "Give the project a name of at most 64 characters.",
  nameTaken: "A project with this name exists already.",
  invalidRepos:
    "Name at least one repository as owner/name or a github.com URL, each once.",
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
