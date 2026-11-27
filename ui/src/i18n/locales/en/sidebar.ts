// The session sidebar: sections, grouping, statuses, the session search.
// Owned by the sidebar area.
export const sidebar = {
  label: "Sessions",
  listLabel: "Session list",
  newSession: "New session",
  search: "Search sessions",
  // The search button's tooltip, with the platform's shortcut.
  searchTitle: "Search sessions ({{shortcut}})",
  shortcutMac: "⌘K",
  shortcutOther: "Ctrl+K",
  searchPlaceholder: "Search by title, project or branch",
  searchEsc: "Esc",
  searchRecent: "Recent sessions",
  searchMatches_one: "{{n}} match",
  searchMatches_other: "{{n}} matches",
  // More matches than the palette lists.
  searchMatchesFirst: "First {{shown}} of {{n}} matches",
  searchMatchesLabel: "Matching sessions",
  searchNone: "No session matches.",
  searchNoSessions: "No sessions yet.",
  groupByStatus: "By status",
  groupByProject: "By project",
  sectionNeeds: "Needs you",
  sectionWorking: "Working",
  sectionDone: "Done",
  sectionIdle: "Idle",
  empty: "Nothing here",
  loading: "Loading sessions…",
  statusApproval: "Approval",
  statusQuestion: "Question",
  statusPlan: "Plan",
  statusWorking: "Working",
  statusQueued: "Check queue",
  statusDone: "Done",
  statusFailed: "Failed",
  statusIdle: "Idle",
  branch: "Branch",
  // After the branch of a session working in more than one repository.
  repoCount_one: "· {{n}} repo",
  repoCount_other: "· {{n}} repos",
  // A session with activity the owner has not seen (its title is bold).
  unseen: "New activity",
};
