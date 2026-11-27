import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import { resources } from "../i18n/resources";
import { QueueBar } from "./QueueBar";

const en = resources.en;

describe("QueueBar", () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  const queue = [
    { messageId: "m1", text: "Then open a draft PR" },
    { messageId: "m/2", text: "And tag it" },
  ];

  it("shows nothing for an empty queue", () => {
    const { container } = render(<QueueBar sessionId="s1" queue={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("sends one now and removes another", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    render(<QueueBar sessionId="s1" queue={queue} />);
    const rows = screen.getAllByText(en.composer.queued);
    expect(rows).toHaveLength(2);
    const sendNow = screen.getAllByRole("button", {
      name: en.composer.sendNow,
    });
    const remove = screen.getAllByRole("button", { name: en.composer.remove });
    fireEvent.click(sendNow[0]);
    fireEvent.click(remove[1]);
    expect(fetchMock.mock.calls.map((c) => [c[0], c[1]?.method])).toEqual([
      ["/api/sessions/s1/queue/m1/send-now", "POST"],
      ["/api/sessions/s1/queue/m%2F2", "DELETE"],
    ]);
    // Until the stream drops them, their buttons stay off.
    await waitFor(() => expect(sendNow[0]).toBeDisabled());
    expect(remove[1]).toBeDisabled();
  });

  it("says why an action failed and lets it be tried again", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "not_implemented" }), {
        status: 501,
      }),
    );
    render(<QueueBar sessionId="s1" queue={queue.slice(0, 1)} />);
    const sendNow = screen.getByRole("button", { name: en.composer.sendNow });
    fireEvent.click(sendNow);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.not_implemented,
    );
    expect(sendNow).toBeEnabled();
  });
});
