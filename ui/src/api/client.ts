import {
  ERROR_CODE_VALUES,
  type AgentCatalog,
  type ApiKey,
  type ApiKeyCreate,
  type AuthSession,
  type Connections,
  type ErrorCode,
  type MessageAccepted,
  type MessageCreate,
  type PlanAnswer,
  type Profile,
  type ProfileUpdate,
  type Project,
  type ProjectCreate,
  type ProjectList,
  type QuestionAnswer,
  type ReplyDecision,
  type Sandbox,
  type Session,
  type SessionCreate,
  type SessionList,
  type SessionUpdate,
} from "./types.gen";

// ApiError is a non-2xx answer (or no answer at all). code is one of the API's
// error codes; the UI shows t(code, { ns: "errors" }), never the detail.
export class ApiError extends Error {
  readonly code: ErrorCode;
  readonly status: number;
  readonly field?: string;

  constructor(code: ErrorCode, status: number, field?: string) {
    super(code);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.field = field;
  }
}

function isErrorCode(value: unknown): value is ErrorCode {
  return ERROR_CODE_VALUES.includes(value as ErrorCode);
}

// errorCode maps anything a call may throw to an error code: a network failure
// (fetch rejects) or a body without a known code reads as internal; the errors
// namespace has a sentence for exactly the API's codes.
export function errorCode(err: unknown): ErrorCode {
  return err instanceof ApiError ? err.code : "internal";
}

// answerLost: the request may have been carried out although no answer says
// so (no answer at all, or a server or proxy failure); a 501 is a refusal.
export function answerLost(err: unknown): boolean {
  if (!(err instanceof ApiError)) return true;
  if (err.code === "not_implemented") return false;
  return err.status === 0 || err.status >= 500;
}

async function failure(res: Response): Promise<ApiError> {
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    // Not JSON (a proxy's HTML page, an empty body): no code to read.
  }
  const b = (body ?? {}) as { code?: unknown; field?: unknown };
  const code = isErrorCode(b.code) ? b.code : "internal";
  const field = typeof b.field === "string" ? b.field : undefined;
  return new ApiError(code, res.status, field);
}

const unauthenticatedListeners = new Set<() => void>();

// onUnauthenticated calls fn whenever an API call answers unauthenticated (the
// session expired or was cleared): the shell swaps to its sign-in screen.
// Returns the unsubscribe.
export function onUnauthenticated(fn: () => void): () => void {
  unauthenticatedListeners.add(fn);
  return () => {
    unauthenticatedListeners.delete(fn);
  };
}

// request is the one fetch every call goes through: JSON in, JSON out, a
// non-2xx answer thrown as ApiError. 204 and 202-without-body give undefined.
export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
    });
  } catch {
    throw new ApiError("internal", 0);
  }
  if (!res.ok) {
    const err = await failure(res);
    if (err.code === "unauthenticated") {
      unauthenticatedListeners.forEach((fn) => fn());
    }
    throw err;
  }
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  return (text === "" ? undefined : JSON.parse(text)) as T;
}

const seg = encodeURIComponent;

// LOGIN_PATH starts a sign-in: a browser navigation, not a fetch (it
// redirects to the identity provider).
export const LOGIN_PATH = "/api/auth/login";

export function getAuthSession(): Promise<AuthSession> {
  return request<AuthSession>("GET", "/api/auth/session");
}

export function logout(): Promise<void> {
  return request<void>("POST", "/api/auth/logout");
}

export function listProjects(): Promise<ProjectList> {
  return request<ProjectList>("GET", "/api/projects");
}

export function listSessions(): Promise<SessionList> {
  return request<SessionList>("GET", "/api/sessions");
}

export function getSession(sessionId: string): Promise<Session> {
  return request<Session>("GET", `/api/sessions/${seg(sessionId)}`);
}

// createSession starts a session: worktrees on fresh origin/<default branch>,
// the agent, and the message as its first turn.
export function createSession(session: SessionCreate): Promise<Session> {
  return request<Session>("POST", "/api/sessions", session);
}

// listAgents is the agent catalog: providers with their models and whether
// they can run now, and the permission modes.
export function listAgents(): Promise<AgentCatalog> {
  return request<AgentCatalog>("GET", "/api/agents");
}

export function sendMessage(
  sessionId: string,
  message: MessageCreate,
): Promise<MessageAccepted> {
  return request<MessageAccepted>(
    "POST",
    `/api/sessions/${seg(sessionId)}/messages`,
    message,
  );
}

export function replyPermission(
  sessionId: string,
  requestId: string,
  decision: ReplyDecision,
): Promise<void> {
  return request<void>(
    "POST",
    `/api/sessions/${seg(sessionId)}/permissions/${seg(requestId)}`,
    { decision },
  );
}

// replyQuestion answers a question: the chosen options per question, or the
// text typed in the composer instead.
export function replyQuestion(
  sessionId: string,
  requestId: string,
  answer: QuestionAnswer,
): Promise<void> {
  return request<void>(
    "POST",
    `/api/sessions/${seg(sessionId)}/questions/${seg(requestId)}`,
    answer,
  );
}

// replyPlan approves a plan (optionally in another mode) or keeps planning
// with feedback.
export function replyPlan(
  sessionId: string,
  requestId: string,
  answer: PlanAnswer,
): Promise<void> {
  return request<void>(
    "POST",
    `/api/sessions/${seg(sessionId)}/plans/${seg(requestId)}`,
    answer,
  );
}

// updateSession changes the title, model or permission mode; absent fields
// stay as they are.
export function updateSession(
  sessionId: string,
  update: SessionUpdate,
): Promise<Session> {
  return request<Session>("PATCH", `/api/sessions/${seg(sessionId)}`, update);
}

// markSessionSeen: the owner has seen the session up to seq.
export function markSessionSeen(sessionId: string, seq: number): Promise<void> {
  return request<void>("POST", `/api/sessions/${seg(sessionId)}/seen`, {
    seq,
  });
}

// sendQueuedNow interrupts the running turn and sends the queued message.
export function sendQueuedNow(
  sessionId: string,
  messageId: string,
): Promise<void> {
  return request<void>(
    "POST",
    `/api/sessions/${seg(sessionId)}/queue/${seg(messageId)}/send-now`,
  );
}

// removeQueued drops a message from the session's queue.
export function removeQueued(
  sessionId: string,
  messageId: string,
): Promise<void> {
  return request<void>(
    "DELETE",
    `/api/sessions/${seg(sessionId)}/queue/${seg(messageId)}`,
  );
}

// archiveSession stops the session's host and removes its worktrees (one
// with uncommitted work stays on disk); its log stays readable.
export function archiveSession(sessionId: string): Promise<void> {
  return request<void>("DELETE", `/api/sessions/${seg(sessionId)}`);
}

export function interruptTurn(sessionId: string): Promise<void> {
  return request<void>("POST", `/api/sessions/${seg(sessionId)}/interrupt`);
}

export function getProfile(): Promise<Profile> {
  return request<Profile>("GET", "/api/profile");
}

// updateProfile changes the fields given; an empty string clears a text
// field.
export function updateProfile(update: ProfileUpdate): Promise<Profile> {
  return request<Profile>("PATCH", "/api/profile", update);
}

export function getConnections(): Promise<Connections> {
  return request<Connections>("GET", "/api/connections");
}

// createApiKey stores or replaces a secret; the answer never carries it.
export function createApiKey(key: ApiKeyCreate): Promise<ApiKey> {
  return request<ApiKey>("POST", "/api/connections/api-keys", key);
}

export function deleteApiKey(keyId: string): Promise<void> {
  return request<void>("DELETE", `/api/connections/api-keys/${seg(keyId)}`);
}

export function createProject(project: ProjectCreate): Promise<Project> {
  return request<Project>("POST", "/api/projects", project);
}

export function deleteProject(projectId: string): Promise<void> {
  return request<void>("DELETE", `/api/projects/${seg(projectId)}`);
}

// SandboxAction is what the owner can do with a project's sandbox.
export type SandboxAction = "start" | "stop" | "rebuild";

export function sandboxAction(
  projectId: string,
  action: SandboxAction,
): Promise<Sandbox> {
  return request<Sandbox>(
    "POST",
    `/api/projects/${seg(projectId)}/sandbox/${action}`,
  );
}
