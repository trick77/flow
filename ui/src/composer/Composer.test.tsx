import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import { resources } from "../i18n/resources";
import { Composer } from "./Composer";

const en = resources.en;

function reply(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
  });
}

describe("Composer", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("sends on Enter with a fresh client id and clears the box", async () => {
    vi.spyOn(crypto, "randomUUID").mockReturnValue(
      "0193a8f2-0000-7000-8000-000000000001",
    );
    let answer: (r: Response) => void = () => {};
    fetchMock.mockReturnValueOnce(new Promise((r) => (answer = r)));
    render(<Composer sessionId="s1" running={false} />);
    const box = screen.getByLabelText(en.composer.placeholder);
    const send = screen.getByRole("button", { name: en.composer.send });
    expect(send).toBeDisabled();

    fireEvent.change(box, { target: { value: "  Fix it  " } });
    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.keyDown(box, { key: "Enter" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/sessions/s1/messages");
    expect(JSON.parse(String(init?.body))).toEqual({
      clientId: "0193a8f2-0000-7000-8000-000000000001",
      text: "Fix it",
    });
    // While it sends, a second Enter does nothing.
    expect(send).toBeDisabled();
    fireEvent.keyDown(box, { key: "Enter" });
    expect(fetchMock).toHaveBeenCalledTimes(1);

    answer(reply(202, { messageId: "m", queued: false }));
    await waitFor(() => expect(box).toHaveValue(""));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps one client id per draft, so a retry after a lost answer is a duplicate", async () => {
    let n = 0;
    vi.spyOn(crypto, "randomUUID").mockImplementation(
      () =>
        `0193a8f2-0000-7000-8000-00000000000${++n}` as `${string}-${string}-${string}-${string}-${string}`,
    );
    fetchMock.mockRejectedValueOnce(new TypeError("network"));
    fetchMock.mockResolvedValueOnce(
      reply(202, { messageId: "m", queued: false }),
    );
    fetchMock.mockResolvedValueOnce(
      reply(202, { messageId: "m", queued: false }),
    );
    render(<Composer sessionId="s1" running={false} />);
    const box = screen.getByLabelText(en.composer.placeholder);
    const ids = () =>
      fetchMock.mock.calls.map(
        ([, init]) => JSON.parse(String(init?.body)).clientId,
      );

    fireEvent.change(box, { target: { value: "Fix it" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(box).toHaveValue(""));
    expect(ids()).toEqual([ids()[0], ids()[0]]);

    // A new draft is a new message.
    fireEvent.change(box, { target: { value: "Next" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(ids()[2]).not.toBe(ids()[0]);
  });

  it("shows an API error and keeps the text", async () => {
    fetchMock.mockResolvedValueOnce(reply(409, { code: "session_archived" }));
    render(<Composer sessionId="s1" running={false} />);
    const box = screen.getByLabelText(en.composer.placeholder);
    fireEvent.change(box, { target: { value: "hello" } });
    fireEvent.click(screen.getByRole("button", { name: en.composer.send }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.session_archived,
    );
    expect(box).toHaveValue("hello");
  });

  it("ignores an empty message", () => {
    render(<Composer sessionId="s1" running={false} />);
    const box = screen.getByLabelText(en.composer.placeholder);
    fireEvent.change(box, { target: { value: "   " } });
    fireEvent.keyDown(box, { key: "Enter" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("queues while a turn runs and stops the turn", async () => {
    fetchMock.mockResolvedValueOnce(reply(204));
    fetchMock.mockResolvedValueOnce(reply(501, { code: "not_implemented" }));
    render(<Composer sessionId="s1" running />);
    expect(
      screen.getByRole("button", { name: en.composer.queue }),
    ).toBeInTheDocument();
    const stop = screen.getByRole("button", { name: en.composer.stop });
    expect(stop).toHaveTextContent(en.composer.stopLabel);

    fireEvent.click(stop);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/s1/interrupt");
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST");
    await waitFor(() => expect(stop).toBeEnabled());

    fireEvent.click(stop);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.not_implemented,
    );
  });

  it("shows the session's agent, model and mode read-only without a catalog", () => {
    render(
      <Composer
        sessionId="s1"
        running={false}
        provider="claude"
        model="claude-opus-5-5"
        mode="accept_edits"
        projectName="riptide"
      />,
    );
    expect(screen.getByText("Runs in the riptide sandbox")).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: "Model: Claude Code, claude-opus-5-5" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: "Permission mode: Accept edits" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Model/ })).toBeNull();
  });

  it("passes the session's effort to its picker", () => {
    render(
      <Composer
        sessionId="s1"
        running={false}
        provider="claude"
        model="claude-opus-5-5"
        effort="xhigh"
        mode="ask"
        catalog={{
          providers: [
            {
              provider: "claude",
              available: true,
              models: [{ id: "claude-opus-5-5", label: "Opus 5.5" }],
              efforts: ["low", "high", "xhigh"],
            },
          ],
          modes: ["ask"],
        }}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Effort: Extra high" }),
    ).toBeInTheDocument();
  });

  it("answers the waiting question instead of sending a message", async () => {
    fetchMock.mockResolvedValueOnce(
      reply(409, { code: "request_not_pending" }),
    );
    fetchMock.mockResolvedValueOnce(reply(204));
    render(<Composer sessionId="s1" running questionId="req 1" />);
    const box = screen.getByLabelText(en.composer.answerPlaceholder);
    fireEvent.change(box, { target: { value: " Key on the tree " } });
    fireEvent.click(screen.getByRole("button", { name: en.composer.answer }));
    // A refusal keeps the draft.
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.request_not_pending,
    );
    expect(box).toHaveValue(" Key on the tree ");
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(box).toHaveValue(""));
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([
      "/api/sessions/s1/questions/req%201",
      "/api/sessions/s1/questions/req%201",
    ]);
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({
      text: "Key on the tree",
    });
  });

  it("lists the queue above the box", () => {
    render(
      <Composer
        sessionId="s1"
        running
        queue={[{ messageId: "m1", text: "Then open a draft PR" }]}
      />,
    );
    const group = screen.getByRole("group", { name: en.composer.queueLabel });
    expect(group).toHaveTextContent("Then open a draft PR");
  });

  it("shows the model id without a catalog and skips an unknown mode", () => {
    render(
      <Composer
        sessionId="s1"
        running={false}
        provider="opencode"
        model="qwen"
        mode={"yolo" as never}
      />,
    );
    expect(
      screen.getByRole("img", { name: "Model: OpenCode, qwen" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /Permission mode/ })).toBeNull();
  });

  it("has no stop button while idle", () => {
    render(<Composer sessionId="s1" running={false} />);
    expect(screen.queryByRole("button", { name: en.composer.stop })).toBeNull();
  });
});
