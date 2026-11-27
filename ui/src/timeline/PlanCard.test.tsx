import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import { resources } from "../i18n/resources";
import type { PlanItem } from "./fold";
import { continueModes, defaultContinueMode, PlanCard } from "./PlanCard";

const en = resources.en;

function item(over: Partial<PlanItem> = {}): PlanItem {
  return {
    type: "plan",
    key: "plan:p-1",
    request: {
      requestId: "p-1",
      plan: "# Move the job queue\n\n1. Add migration `0044_pg_jobs.sql`.\n2. Switch with **BACKEND_QUEUE**.",
    },
    open: true,
    ...over,
  };
}

const posted = (fetchMock: ReturnType<typeof vi.fn>) =>
  JSON.parse(String(fetchMock.mock.calls[0][1]?.body));

describe("PlanCard", () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("renders the plan as Markdown and approves in accept edits", async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<PlanCard item={item()} sessionId="s1" mode="plan" />);
    expect(screen.getByText(en.timeline.awaitingApproval)).toHaveClass(
      "pill-sun",
    );
    expect(
      screen.getByRole("heading", { name: "Move the job queue" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.getByText("0044_pg_jobs.sql").tagName).toBe("CODE");
    expect(
      screen.getByRole("button", { name: "Continue in: Accept edits" }),
    ).toBeInTheDocument();
    const approve = screen.getByRole("button", {
      name: en.timeline.approvePlan,
    });
    fireEvent.click(approve);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/s1/plans/p-1");
    expect(posted(fetchMock)).toEqual({
      decision: "approved",
      mode: "accept_edits",
    });
    await waitFor(() => expect(approve).toBeDisabled());
  });

  it("approves in the mode picked, within the cap", () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(
      <PlanCard item={item()} sessionId="s1" mode="plan" modeCap="auto" />,
    );
    fireEvent.click(screen.getByRole("button", { name: /^Continue in/ }));
    const opts = screen.getAllByRole("menuitemradio").map((o) => o.textContent);
    expect(opts).toEqual(["Ask", "Accept edits", "Auto"]);
    fireEvent.click(screen.getByRole("menuitemradio", { name: "Auto" }));
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.approvePlan }),
    );
    expect(posted(fetchMock)).toEqual({ decision: "approved", mode: "auto" });
  });

  it("keeps planning with feedback", () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<PlanCard item={item()} sessionId="s1" />);
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.keepPlanning }),
    );
    const box = screen.getByRole("textbox", { name: en.timeline.planFeedback });
    expect(box).toHaveFocus();
    fireEvent.click(screen.getByRole("button", { name: en.timeline.cancel }));
    expect(screen.queryByRole("textbox")).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.keepPlanning }),
    );
    fireEvent.change(
      screen.getByRole("textbox", { name: en.timeline.planFeedback }),
      { target: { value: " Smaller steps " } },
    );
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.sendFeedback }),
    );
    expect(posted(fetchMock)).toEqual({
      decision: "rejected",
      feedback: "Smaller steps",
    });
  });

  it("rejects without feedback too", () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<PlanCard item={item()} sessionId="s1" />);
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.keepPlanning }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.sendFeedback }),
    );
    expect(posted(fetchMock)).toEqual({ decision: "rejected" });
  });

  it("says why a reply failed and lets it be sent again", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "not_implemented" }), {
        status: 501,
      }),
    );
    render(<PlanCard item={item()} sessionId="s1" />);
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.approvePlan }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.not_implemented,
    );
    expect(
      screen.getByRole("button", { name: en.timeline.approvePlan }),
    ).toBeEnabled();
  });

  it("approves without a mode when none can be offered", () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<PlanCard item={item()} sessionId="s1" modes={["plan"]} />);
    expect(screen.queryByRole("button", { name: /^Continue in/ })).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.approvePlan }),
    );
    expect(posted(fetchMock)).toEqual({ decision: "approved" });
  });

  it("shrinks to its outcome", () => {
    const { rerender } = render(
      <PlanCard
        item={item({
          open: false,
          resolved: { requestId: "p-1", decision: "approved", mode: "auto" },
        })}
        sessionId="s1"
      />,
    );
    expect(document.querySelector(".resolved")?.textContent).toBe(
      `${en.timeline.planApproved}${en.timeline.separator}continues in Auto`,
    );
    rerender(
      <PlanCard
        item={item({
          open: false,
          resolved: { requestId: "p-1", decision: "approved" },
        })}
        sessionId="s1"
      />,
    );
    expect(document.querySelector(".resolved")?.textContent).toBe(
      en.timeline.planApproved,
    );
    rerender(
      <PlanCard
        item={item({
          open: false,
          resolved: {
            requestId: "p-1",
            decision: "rejected",
            feedback: "Smaller steps",
          },
        })}
        sessionId="s1"
      />,
    );
    expect(document.querySelector(".resolved")?.textContent).toBe(
      `${en.timeline.planRejected}${en.timeline.separator}Smaller steps`,
    );
    rerender(
      <PlanCard
        item={item({
          open: false,
          resolved: { requestId: "p-1", decision: "expired" },
        })}
        sessionId="s1"
      />,
    );
    expect(document.querySelector(".resolved.denied")).toHaveTextContent(
      en.timeline.planExpired,
    );
  });

  it("offers the modes after plan, within the cap", () => {
    const all = ["plan", "ask", "accept_edits", "auto", "full_access"] as const;
    expect(continueModes(all)).toEqual([
      "ask",
      "accept_edits",
      "auto",
      "full_access",
    ]);
    expect(continueModes(all, "ask")).toEqual(["ask"]);
    expect(defaultContinueMode(["ask", "auto"], "auto")).toBe("auto");
    expect(defaultContinueMode(["ask", "auto"], "plan")).toBe("ask");
    expect(defaultContinueMode([])).toBeUndefined();
  });
});
