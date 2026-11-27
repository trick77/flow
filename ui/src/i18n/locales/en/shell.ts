// The app shell: header, the start page, sign-in and the account menu.
export const shell = {
  empty: "No sessions yet.",
  emptyHint: "Sessions you start will show up here.",
  session: "Session",
  crumbSeparator: "/",
  branch: "Branch",
  signInHint: "Sign in to continue.",
  signIn: "Sign in",
  signInFailed: "Sign-in did not work. Try again.",
  signOut: "Sign out",
  account: "Account: {{name}}",
  owner: "Owner",
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
  sandboxDeleting: "This session's sandbox is being deleted.",
  sandboxStarting: "Starting the sandbox…",
  startSandbox: "Start sandbox",
};
