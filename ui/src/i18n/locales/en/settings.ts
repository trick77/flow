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
  // GitHub.
  github: "GitHub",
  githubUnavailable: "Not available: this server has no GitHub App configured.",
  githubDisconnectedHint:
    "Connect to let agents push and reach private repositories.",
  githubPendingHint: "A connection was started but not confirmed on GitHub.",
  githubConnectedAs: "Connected as {{login}}",
  githubConnected: "Connected",
  githubSince: "since {{date}}",
  githubRenewBy: "renew by {{date}}",
  githubSeparator: " · ",
  githubExpiredHint:
    "The authorization expired. Agents cannot push or reach private repositories until you reconnect.",
  githubUnknownState: "The connection is in a state this page does not know.",
  pillUnavailable: "Unavailable",
  pillNotConnected: "Not connected",
  pillConnecting: "Connecting",
  pillConnected: "Connected",
  pillReconnect: "Reconnect needed",
  pillError: "Error",
  connect: "Connect",
  starting: "Starting…",
  reconnect: "Reconnect",
  startAgain: "Start again",
  disconnect: "Disconnect",
  disconnecting: "Disconnecting…",
  confirmDisconnect:
    "Disconnect GitHub? Agents can no longer push or reach private repositories. Running sessions keep working with what they already have.",
  // The device flow.
  deviceTitle: "Connect GitHub",
  deviceEnter: "Enter this code on GitHub to connect Flow.",
  deviceCode: "Code to enter on GitHub",
  deviceCopy: "Copy code",
  deviceCopied: "Copied",
  deviceCopyFailed: "Copying failed: select the code and copy it by hand.",
  deviceOpen: "Open GitHub",
  deviceOpenHint: "Opens in a new tab.",
  deviceExpiresIn: "The code expires in {{time}}.",
  deviceTime: "{{minutes}}:{{seconds}}",
  deviceWaiting: "Waiting for your confirmation on GitHub…",
  devicePaused:
    "Paused while this tab is hidden; it goes on when you come back.",
  deviceConnectedAs: "Connected as {{login}}.",
  deviceConnected: "GitHub is connected.",
  deviceDenied:
    "GitHub did not confirm the connection: it was denied or the code is no longer valid.",
  deviceExpired: "The code expired before it was confirmed.",
  deviceFailed: "Connecting failed.",
  tryAgain: "Try again",
};
