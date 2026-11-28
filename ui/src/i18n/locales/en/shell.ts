// The app shell: header, the start page, sign-in and the account menu.
export const shell = {
  empty: "No sessions yet.",
  emptyHint: "Sessions you start will show up here.",
  newSession: "New session",
  session: "Session",
  crumbSeparator: "/",
  branch: "Branch",
  // Renaming the open session from its header title.
  rename: "Rename session",
  renameLabel: "Session title",
  renameFailed: "The session was not renamed.",
  dismiss: "Dismiss",
  signInHint: "Sign in to continue.",
  signIn: "Sign in",
  signInFailed: "Sign-in did not work. Try again.",
  signOut: "Sign out",
  account: "Account: {{name}}",
  owner: "Owner",
  // Under the owner's name in the sidebar foot: the host Flow runs on.
  ownerAt: "Owner · {{host}}",
  // The header's menu button on narrow screens.
  openSidebar: "Open sidebar",
  // The header's sandbox chip, by Sandbox.state.
  sandboxChipNone: "No sandbox",
  sandboxChipSetup: "Sandbox setting up",
  sandboxChipWarm: "Sandbox warm",
  sandboxChipStopping: "Sandbox stopping",
  sandboxChipStopped: "Sandbox stopped",
  sandboxChipFailed: "Sandbox failed",
  sandboxChipDeleting: "Sandbox deleting",
  // setup or stopping with nothing running (Sandbox.busy false).
  sandboxChipSetupStale: "Sandbox setup did not finish",
  sandboxChipStopStale: "Sandbox stop did not finish",
  // The header's repositories menu of a session in several repositories.
  repoCount_one: "{{n}} repo",
  repoCount_other: "{{n}} repos",
  reposLabel_one: "{{n}} repository in this session",
  reposLabel_other: "{{n}} repositories in this session",
  reposMenu: "Repositories in this session",
  reposOnBranch: "Worktrees on",
  reposHint: "Select a repository to open its diff",
  repoNoWorktree: "worktree created with the first message",
  // The toggle of the review panel beside the timeline.
  togglePanel: "Toggle diff panel",
  // Phones: the diff panel and Archive in one menu of the session header.
  more: "More actions",
  moreMenu: "Session actions",
  panelItem: "Diff panel",
  panelTitle: "Diff",
  projects: "Projects",
  settings: "Settings",
  backToSession: "Back to session",
  // Archiving the open session from its header.
  archive: "Archive session",
  confirmArchive:
    "Archive {{title}}? Its agent stops and its worktrees are removed; a worktree with uncommitted or untracked changes is kept on disk with its branch. Its log is kept.",
  confirmArchiveAction: "Archive",
  archiving: "Archiving…",
  cancel: "Cancel",
  // In place of a session's history while its sandbox does not run.
  sandboxStopped:
    "This session's sandbox is stopped. Its history and agent come back when it runs.",
  sandboxFailed:
    "This session's sandbox failed. Start it again to see its history and continue.",
  sandboxStopping: "This session's sandbox is stopping.",
  sandboxSetupStale:
    "This session's sandbox did not finish setting up. Start it again to see its history and continue.",
  sandboxStopStale:
    "This session's sandbox did not finish stopping. Start it again to see its history and continue.",
  sandboxDeleting: "This session's sandbox is being deleted.",
  sandboxStarting: "Starting the sandbox…",
  startSandbox: "Start sandbox",
};
