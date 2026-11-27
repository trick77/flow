// The session sidebar: grouping by status or project, started sessions
// indented under their parent, search, the rail. Strings: sidebar namespace.
// Owner: shell and sidebar area (docs/ownership.md).
export { groupSessions, orderSessions, sectionOf } from "./group";
export { Sidebar } from "./Sidebar";
export { useGrouping } from "./useGrouping";
export {
  useSessions,
  type SessionPatch,
  type SessionsState,
} from "./useSessions";
