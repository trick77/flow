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
};
