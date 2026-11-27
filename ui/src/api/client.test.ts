import {
  ApiError,
  errorCode,
  getAuthSession,
  getSession,
  interruptTurn,
  listProjects,
  listSessions,
  logout,
  onUnauthenticated,
  replyPermission,
  sendMessage,
} from ".";

function respond(status: number, body?: unknown, raw?: string): Response {
  const text = raw ?? (body === undefined ? "" : JSON.stringify(body));
  return new Response(status === 204 ? null : text, { status });
}

describe("API client", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads JSON lists", async () => {
    fetchMock.mockResolvedValueOnce(respond(200, { items: [] }));
    fetchMock.mockResolvedValueOnce(respond(200, { items: [{ id: "p" }] }));
    await expect(listSessions()).resolves.toEqual({ items: [] });
    await expect(listProjects()).resolves.toEqual({ items: [{ id: "p" }] });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions");
    expect(fetchMock.mock.calls[0][1]?.method).toBe("GET");
    expect(fetchMock.mock.calls[0][1]?.body).toBeUndefined();
    expect(fetchMock.mock.calls[1][0]).toBe("/api/projects");
  });

  it("escapes ids in paths", async () => {
    fetchMock.mockResolvedValueOnce(respond(200, { lastSeq: 3 }));
    await getSession("a/b");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/a%2Fb");
  });

  it("posts a message as JSON", async () => {
    fetchMock.mockResolvedValueOnce(
      respond(202, { messageId: "m1", queued: false }),
    );
    const accepted = await sendMessage("s1", { clientId: "m1", text: "hi" });
    expect(accepted).toEqual({ messageId: "m1", queued: false });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/sessions/s1/messages");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      clientId: "m1",
      text: "hi",
    });
    expect((init?.headers as Record<string, string>)["Content-Type"]).toBe(
      "application/json",
    );
  });

  it("answers a permission and interrupts with 204", async () => {
    fetchMock.mockResolvedValueOnce(respond(204));
    fetchMock.mockResolvedValueOnce(respond(204));
    await expect(
      replyPermission("s1", "perm-1", "allow_always"),
    ).resolves.toBeUndefined();
    await expect(interruptTurn("s1")).resolves.toBeUndefined();
    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/sessions/s1/permissions/perm-1",
    );
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      decision: "allow_always",
    });
    expect(fetchMock.mock.calls[1][0]).toBe("/api/sessions/s1/interrupt");
  });

  it("treats an empty 2xx body as undefined", async () => {
    fetchMock.mockResolvedValueOnce(respond(202));
    await expect(sendMessage("s", { clientId: "c", text: "t" })).resolves.toBe(
      undefined,
    );
  });

  it("throws the error code of a non-2xx answer", async () => {
    fetchMock.mockResolvedValueOnce(
      respond(409, { code: "session_archived", field: "text" }),
    );
    const err = await sendMessage("s", { clientId: "c", text: "t" }).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      code: "session_archived",
      status: 409,
      field: "text",
    });
    expect(errorCode(err)).toBe("session_archived");
  });

  it("reads an unknown code, a non-JSON body and a network failure as internal", async () => {
    fetchMock.mockResolvedValueOnce(respond(500, { code: "brand_new" }));
    fetchMock.mockResolvedValueOnce(respond(502, undefined, "<html>"));
    fetchMock.mockRejectedValueOnce(new TypeError("offline"));
    for (let i = 0; i < 3; i++) {
      const err = await listSessions().catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect(errorCode(err)).toBe("internal");
    }
    expect(errorCode(new Error("x"))).toBe("internal");
  });

  it("reads the auth session and signs out", async () => {
    fetchMock.mockResolvedValueOnce(
      respond(200, { signedIn: true, mode: "oidc", name: "Owner" }),
    );
    fetchMock.mockResolvedValueOnce(respond(204));
    await expect(getAuthSession()).resolves.toEqual({
      signedIn: true,
      mode: "oidc",
      name: "Owner",
    });
    await expect(logout()).resolves.toBeUndefined();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/auth/session");
    expect(fetchMock.mock.calls[1][0]).toBe("/api/auth/logout");
    expect(fetchMock.mock.calls[1][1]?.method).toBe("POST");
  });

  it("tells listeners about unauthenticated answers only", async () => {
    const heard = vi.fn();
    const off = onUnauthenticated(heard);
    fetchMock.mockResolvedValueOnce(respond(403, { code: "forbidden" }));
    fetchMock.mockResolvedValueOnce(respond(401, { code: "unauthenticated" }));
    fetchMock.mockResolvedValueOnce(respond(401, { code: "unauthenticated" }));
    await listSessions().catch(() => {});
    expect(heard).not.toHaveBeenCalled();
    await listSessions().catch(() => {});
    expect(heard).toHaveBeenCalledTimes(1);
    off();
    await listSessions().catch(() => {});
    expect(heard).toHaveBeenCalledTimes(1);
  });
});
