import {
  ERROR_CODE_VALUES,
  type ErrorCode,
  type MessageAccepted,
  type MessageCreate,
  type ProjectList,
  type ReplyDecision,
  type Session,
  type SessionList,
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
    throw await failure(res);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  return (text === "" ? undefined : JSON.parse(text)) as T;
}

const seg = encodeURIComponent;

export function listProjects(): Promise<ProjectList> {
  return request<ProjectList>("GET", "/api/projects");
}

export function listSessions(): Promise<SessionList> {
  return request<SessionList>("GET", "/api/sessions");
}

export function getSession(sessionId: string): Promise<Session> {
  return request<Session>("GET", `/api/sessions/${seg(sessionId)}`);
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

export function interruptTurn(sessionId: string): Promise<void> {
  return request<void>("POST", `/api/sessions/${seg(sessionId)}/interrupt`);
}
