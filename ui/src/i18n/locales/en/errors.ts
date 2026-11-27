import type { ErrorCode } from "../../../api/types.gen";

// One sentence per API error code (backend/api/openapi.json, ErrorCode). The
// type makes a missing or stale code a compile error.
export const errors: Record<ErrorCode, string> = {
  not_implemented: "This part of Flow is not built yet.",
  unauthenticated: "You are signed out. Sign in again.",
  forbidden: "This account may not use Flow.",
  preview_origin: "Requests from a preview page are not allowed.",
  not_found: "Not found. It may have been deleted.",
  invalid_request: "The request was not valid.",
  conflict: "That changed in the meantime. Reload and try again.",
  internal: "Something went wrong on the server.",
  upstream: "GitHub or the sandbox did not answer as expected.",
  github_not_connected: "Connect GitHub in Settings first.",
  sandbox_unavailable: "The sandbox is not reachable right now.",
  sandbox_busy: "The sandbox is busy with setup. Try again shortly.",
  session_archived: "This session is archived.",
  request_not_pending: "This request was already answered or has expired.",
  mode_above_cap:
    "This session cannot use a mode above the one its starting session allows.",
  provider_unavailable: "This agent is not signed in or has no API key.",
  repo_in_use: "A session still works in this repository.",
  too_many_sessions: "Too many sessions are running. Wait for one to finish.",
  sandbox_network_missing:
    "The sandbox network is missing on the server. Run the host setup again.",
  sandbox_memory_full:
    "The running sandboxes use all the memory Flow may give them. Stop one first.",
};
