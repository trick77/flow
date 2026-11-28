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
  // The palette's projects, each starting a new session in it.
  searchProjects: "Projects",
  groupLabel: "Group sessions",
  groupByStatus: "By status",
  groupByProject: "By project",
  // The section, grouped by project, of sessions whose project is not known.
  otherProjects: "Other",
  // Collapsing the sidebar to its icon rail, and the drawer on narrow screens.
  collapse: "Collapse sidebar",
  expand: "Expand sidebar",
  closeSidebar: "Close sidebar",
  railLabel: "Sessions (compact)",
  railSession: "{{project}}: {{title}}",
  railSessionStatus: "{{project}}: {{title}} ({{status}})",
  // An idle row's last activity less than a minute ago.
  timeNow: "now",
  sectionNeeds: "Needs you",
  sectionWorking: "Working",
  sectionDone: "Done",
  sectionIdle: "Idle",
  empty: "Nothing here",
  // Grouped by project: a project without sessions, its "+" button, and the
  // list without any project.
  noSessionsYet: "No sessions yet",
  newSessionIn: "New session in {{project}}",
  noProjects: "No projects yet.",
  addProject: "Add a project",
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
