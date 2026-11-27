// The Settings page. Owned by the projects and settings pages area.
export const settings = {
  title: "Settings",
  lead: "Applies to every project and session on this server.",
  loadFailed: "The settings could not be loaded.",
  // Profile.
  profile: "Profile",
  profileHint:
    "Used as author for commits the agents make in your worktrees. New sessions get it; running ones keep theirs.",
  gitName: "Git name",
  gitNamePlaceholder: "Your name",
  gitEmail: "Git email",
  gitEmailPlaceholder: "name@example.com",
  identityHalf:
    "Sessions get a git identity only when both name and email are set.",
  save: "Save",
  saving: "Saving…",
  saved: "Saved",
  invalidGitName:
    "Use at most 200 characters, without line breaks or control characters.",
  invalidGitEmail: "Enter an address like name@example.com.",
  // Connections.
  connections: "Connections",
  connectionsHint:
    "Secrets stay on this server, encrypted, and reach only your own sandboxes. Flow never shows a stored value again.",
  claudeToken: "Claude subscription token",
  claudeTokenHint:
    "Run claude setup-token on any machine and paste the token here. Used by sessions in subscription token mode.",
  anthropicKey: "Anthropic API key",
  anthropicKeyHint: "Used only by sessions in API key mode.",
  stored: "Stored",
  notStored: "Not stored",
  storedSince: "Stored {{date}}",
  replace: "Replace",
  add: "Add",
  remove: "Delete",
  removing: "Deleting…",
  cancel: "Cancel",
  storeAction: "Store",
  storing: "Storing…",
  claudeTokenPlaceholder: "Paste the token",
  anthropicKeyPlaceholder: "Paste the key",
  invalidSecret:
    "Paste the whole value on one line; at most {{max}} characters.",
  confirmRemove:
    "Delete the stored {{what}}? Sessions that use it stop working until you store another.",
};
