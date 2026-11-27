// The API client: typed fetch over types.gen.ts, error codes to the errors
// namespace, EventSource streams by seq. Owner: API client area
// (docs/ownership.md).
export {
  ApiError,
  errorCode,
  getAuthSession,
  getSession,
  interruptTurn,
  listProjects,
  listSessions,
  LOGIN_PATH,
  logout,
  onUnauthenticated,
  replyPermission,
  request,
  sendMessage,
} from "./client";
export {
  onStreamGaveUp,
  streamAppEvents,
  streamSessionEvents,
  type AppStreamOptions,
  type EventSourceCtor,
  type EventSourceLike,
  type SessionStreamOptions,
  type StreamOptions,
} from "./stream";
