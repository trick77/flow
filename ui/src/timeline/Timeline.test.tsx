import { act, fireEvent, render, screen, within } from "@testing-library/react";

import type { TypedEvent } from "../contract/events.gen";
import { resources } from "../i18n/resources";
import { FakeEventSource } from "../test/fakeEventSource";
import { fixtureEvents, fixtureLines, mk } from "../test/flowEvents";
import { foldEvents, type Folded } from "./fold";
import { emptyStream, reduceStream, type StreamState } from "./state";
import { Timeline } from "./Timeline";
import { useSessionStream } from "./useSessionStream";

const en = resources.en;

function stateOf(events: TypedEvent[], live: StreamState["live"] = []) {
  const s = events.reduce<StreamState>(
    (acc, event) => reduceStream(acc, { type: "event", event }),
    emptyStream,
  );
  return { ...s, live };
}

function show(folded: Folded, working = false) {
  return render(<Timeline sessionId="s1" folded={folded} working={working} />);
}

function row(key: string): HTMLElement {
  return screen.getByTestId(key);
}

describe("Timeline", () => {
  const demo = fixtureEvents("demo.jsonl");

  it("shows an open question and plan with the session's context", () => {
    const onAnswerInComposer = vi.fn();
    const folded = foldEvents(
      stateOf([
        mk("turn.started", { messageId: "m" }, { seq: 1, turn: "t" }),
        mk(
          "question.asked",
          {
            requestId: "q",
            questions: [
              {
                id: "a",
                text: "Which key?",
                multiSelect: false,
                options: [{ id: "o", label: "SHA" }],
              },
            ],
          },
          { seq: 2, turn: "t" },
        ),
        mk(
          "plan.proposed",
          { requestId: "p", plan: "1. Go" },
          { seq: 3, turn: "t" },
        ),
      ]),
    );
    render(
      <Timeline
        sessionId="s1"
        folded={folded}
        working={false}
        modes={["plan", "ask", "auto"]}
        modeCap="ask"
        mode="plan"
        onAnswerInComposer={onAnswerInComposer}
      />,
    );
    expect(row("question:q")).toHaveTextContent("Which key?");
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.answerInComposer }),
    );
    expect(onAnswerInComposer).toHaveBeenCalled();
    // Capped at ask: the only mode to continue in, no picker.
    expect(row("plan:p")).toHaveTextContent("Go");
    expect(screen.queryByRole("button", { name: /^Continue in/ })).toBeNull();
  });

  it("renders the finished demo turn, its fold collapsed until opened", () => {
    show(foldEvents(stateOf(demo)));
    expect(screen.getByRole("log", { name: en.timeline.label })).toBeVisible();
    expect(screen.getByText(/clamp it server-side/)).toBeInTheDocument();
    const fold = screen.getByRole("button", { name: "Worked for 3m 41s" });
    expect(fold).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("tool:toolu_d1")).toBeNull();

    fireEvent.click(fold);
    expect(fold).toHaveAttribute("aria-expanded", "true");
    expect(row("tool:toolu_d1")).toHaveTextContent(
      "Readscener/backend/internal/httpapi/prefs.go",
    );
    expect(row("tool:toolu_d2")).toHaveTextContent(
      "Searched for--ui-sidebar-pref in scener/ui/src9 results",
    );
    expect(row("tool:toolu_d3")).toHaveTextContent("Created");
    expect(row("tool:toolu_d3")).toHaveTextContent(/\+5$/);
    expect(row("tool:toolu_d4")).toHaveTextContent("+15 −4");
    expect(row("tool:toolu_d6")).toHaveTextContent("Ranmake check");
    expect(row("gate:22")).toHaveTextContent(
      // toHaveTextContent folds the en spaces around the middot to spaces.
      "Ran the checksdev/scenerpassed · 1m 01s",
    );
    expect(row("gate:22").querySelector(".tsrc.ok")).not.toBeNull();

    // Todo list, resolved permission, answer table, files card.
    expect(screen.getByText(en.timeline.todos)).toBeInTheDocument();
    expect(screen.getByText("0 of 4")).toBeInTheDocument();
    expect(
      screen.getByText("Adding the sidebar_width column").closest("li"),
    ).toHaveClass("now");
    expect(document.querySelector(".resolved")).toHaveTextContent(
      "Allowed once make check",
    );
    expect(screen.getByRole("table")).toHaveTextContent("1'284 passed");
    const files = screen.getByRole("button", { name: /3 files changed/ });
    expect(files).toHaveTextContent("+31 −7");
    expect(screen.queryByText("ui/src/chat/SidebarResizer.tsx")).toBeNull();
    fireEvent.click(files);
    const list = document.querySelector(".flist") as HTMLElement;
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    expect(within(list).getByText(en.timeline.fileChange.added)).toBeVisible();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("shows a running turn open, with a spinner and the working indicator", () => {
    show(foldEvents(stateOf(demo.slice(0, 21))), true);
    const fold = screen.getByRole("button", { name: /^Working for / });
    expect(fold).toHaveAttribute("aria-expanded", "true");
    expect(row("tool:toolu_d6")).toHaveTextContent("Runningmake check");
    expect(row("tool:toolu_d6").querySelector(".spin")).not.toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Working… · Run the gate",
    );
    // An open card for the request that is not resolved yet.
    const asking = foldEvents(stateOf(demo.slice(0, 18)));
    const { container } = show(asking, true);
    expect(container.querySelector(".perm")).not.toBeNull();
    expect(within(container).getByRole("status")).toHaveTextContent(
      /^Working…$/,
    );
  });

  it("opens a command row to the whole command, wrapped", () => {
    const command = `go test -race -count=50 -run 'TestMigrations' ./${"pkg/".repeat(40)}...`;
    show(
      foldEvents(
        stateOf([
          mk("turn.started", { messageId: "m" }, { seq: 1, turn: "t" }),
          mk(
            "tool.started",
            { toolId: "b1", name: "Bash", kind: "bash", command },
            { seq: 2, turn: "t" },
          ),
        ]),
      ),
      true,
    );
    const r = row("tool:b1");
    const toggle = within(r).getByRole("button", { name: /^Running/ });
    expect(r.querySelector("pre")).toBeNull();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const pre = r.querySelector("pre") as HTMLElement;
    expect(pre).toHaveClass("cmd-wrap");
    expect(pre.textContent).toBe(command);
  });

  it("shows thinking estimates as one Thinking indicator, not as rows", () => {
    const pt = (raw: unknown, seq: number) =>
      mk(
        "provider.passthrough",
        { provider: "claude", name: "system/thinking_tokens", raw },
        { seq, turn: "t" },
      );
    show(
      foldEvents(
        stateOf([
          mk("turn.started", { messageId: "m" }, { seq: 1, turn: "t" }),
          pt({ estimated_tokens: 40 }, 2),
          pt({ estimated_tokens: 1834 }, 3),
        ]),
      ),
      true,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Thinking… · about 1'834 tokens",
    );
    expect(screen.queryByText(en.timeline.passthrough)).toBeNull();
    expect(
      screen.getByRole("button", { name: /^Working for / }),
    ).toBeDisabled();
  });

  it("renders the kind fixtures: errors, subagents, gates, dividers, notices", () => {
    const events = fixtureEvents("events.jsonl");
    show(foldEvents({ ...emptyStream, events }));
    fireEvent.click(screen.getByRole("button", { name: "Worked for 6m 40s" }));
    expect(row("tool:toolu_03")).toHaveTextContent("2 of 20 failed");
    expect(row("tool:toolu_03").querySelector(".tsrc.bad")).not.toBeNull();
    expect(row("tool:toolu_05")).toHaveTextContent("Subagentreview migrations");
    expect(row("tool:toolu_06")).toHaveClass("nested");
    expect(row("gate:24")).toHaveTextContent("Ran the checksdev/riptidepassed");
    expect(screen.getByRole("separator")).toHaveTextContent(
      "Session resumed · 26.11.2026",
    );
    expect(
      screen.getByText(en.timeline.notices.context_compacted),
    ).toBeInTheDocument();
    expect(screen.getByText(en.timeline.fromChild)).toBeInTheDocument();
    expect(screen.getByText("2 of 4")).toBeInTheDocument();
    expect(screen.getByText(/testdb.New hands every/)).toBeInTheDocument();
  });

  it("summarizes tool results and outcomes", () => {
    const t = "x";
    const events = [
      mk("turn.started", { messageId: "m" }, { seq: 1, turn: t }),
      mk(
        "tool.started",
        { toolId: "a", name: "Bash", kind: "bash", command: "false" },
        { seq: 2, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "a", status: "ok", result: { exitCode: 2 } },
        { seq: 3, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "b", name: "Bash", kind: "bash", description: "tests" },
        { seq: 4, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "b", status: "ok", result: { testsPassed: 24 } },
        { seq: 5, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "c", name: "Read", kind: "read", path: "a.go" },
        { seq: 6, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "c", status: "denied" },
        { seq: 7, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "d", name: "WebFetch", kind: "web", url: "https://x.test" },
        { seq: 8, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "d", status: "interrupted" },
        { seq: 9, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "e", name: "mcp__gh__pr", kind: "mcp" },
        { seq: 10, turn: t },
      ),
      mk(
        "tool.finished",
        {
          toolId: "e",
          status: "ok",
          result: { pullRequest: { repo: "o/r", number: 212 } },
        },
        { seq: 11, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "f", name: "Teleport", kind: "teleport" as "other" },
        { seq: 12, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "f", status: "error" },
        { seq: 13, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "g", name: "TodoWrite", kind: "todo" },
        { seq: 14, turn: t },
      ),
      mk("tool.finished", { toolId: "g", status: "ok" }, { seq: 15, turn: t }),
      mk(
        "tool.started",
        { toolId: "h", name: "Glob", kind: "search", path: "src" },
        { seq: 16, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "h", status: "ok", result: { matches: 1 } },
        { seq: 17, turn: t },
      ),
      mk(
        "tool.started",
        { toolId: "i", name: "Edit", kind: "edit", path: "b.go" },
        { seq: 18, turn: t },
      ),
      mk(
        "tool.finished",
        { toolId: "i", status: "error", result: { testsPassed: 3 } },
        { seq: 19, turn: t },
      ),
      mk("gate.queued", { position: 2 }, { seq: 20, turn: t }),
      mk(
        "turn.ended",
        { outcome: "interrupted", durationMs: 3_725_000 },
        { seq: 21, turn: t },
      ),
      mk("gate.started", { repo: "o/r" }, { seq: 22 }),
      mk(
        "gate.finished",
        { repo: "o/r", exitCode: 2, durationMs: 4000 },
        { seq: 23 },
      ),
      mk("notice", { code: "brand_new", level: "warning" }, { seq: 24 }),
      mk(
        "message.user",
        { messageId: "p", text: "From above", origin: "parent_session" },
        { seq: 25 },
      ),
    ];
    show(foldEvents(stateOf(events)));
    const fold = screen.getByRole("button", {
      name: "Worked for 1h 02m · interrupted",
    });
    fireEvent.click(fold);
    expect(row("tool:a")).toHaveTextContent("Ranfalseexit 2");
    expect(row("tool:b")).toHaveTextContent("Rantests24 passed");
    expect(row("tool:b").querySelector(".tsrc.ok")).not.toBeNull();
    expect(row("tool:c")).toHaveTextContent("Reada.godenied");
    expect(row("tool:d")).toHaveTextContent("Fetchedhttps://x.testinterrupted");
    expect(row("tool:e")).toHaveTextContent(
      "Calledmcp__gh__prpull request #212",
    );
    expect(row("tool:f")).toHaveTextContent("UsedTeleportfailed");
    expect(row("tool:g")).toHaveTextContent(/^Updated the todo list$/);
    expect(row("tool:h")).toHaveTextContent("Searched forsrc1 result");
    expect(row("tool:i").querySelector(".tsrc.bad")).toHaveTextContent(
      "3 passed",
    );
    expect(row("gate:20")).toHaveTextContent(
      "Waiting for the check queueposition 2",
    );
    // The turnless gate gets its own fold.
    fireEvent.click(screen.getByRole("button", { name: en.timeline.activity }));
    expect(row("gate:22")).toHaveTextContent("Ran the checkso/rfailed");
    expect(screen.getByText("Notice: brand_new")).toBeInTheDocument();
    expect(screen.getByText(en.timeline.fromParent)).toBeInTheDocument();
  });

  it("shows live text while it is written", () => {
    const folded = foldEvents(
      stateOf([], [{ blockId: "b", text: "Half a **sentence** and `co" }]),
    );
    const { container } = show(folded, true);
    expect(container.querySelector(".answer.live")).toHaveTextContent(
      "Half a sentence and `co",
    );
  });

  it("disables the toggle of a fold without rows", () => {
    show(
      foldEvents(
        stateOf([
          mk("turn.started", { messageId: "m" }, { seq: 1, turn: "t" }),
          mk(
            "turn.ended",
            { outcome: "completed", durationMs: 41000 },
            { seq: 2, turn: "t" },
          ),
        ]),
      ),
    );
    expect(
      screen.getByRole("button", { name: "Worked for 41s" }),
    ).toBeDisabled();
  });

  it("shows queued gates after another repo", () => {
    show(
      foldEvents(
        stateOf([
          mk(
            "gate.queued",
            { position: 3, repo: "a/b", aheadRepo: "c/d" },
            { seq: 1, turn: "t" },
          ),
          mk("turn.started", { messageId: "m" }, { seq: 2, turn: "t" }),
        ]),
      ),
    );
    expect(row("gate:1")).toHaveTextContent("position 3, after c/d");
  });
});

function Probe({
  id,
  onStatus,
}: {
  id: string;
  onStatus: (s: string) => void;
}) {
  const state = useSessionStream(id, {
    EventSource: FakeEventSource,
    onStatus,
  });
  return (
    <div>
      <span data-testid="seq">{state.lastSeq}</span>
      <span data-testid="live">{state.live.map((b) => b.text).join("|")}</span>
    </div>
  );
}

describe("useSessionStream", () => {
  beforeEach(() => FakeEventSource.reset());

  it("feeds the reducer, reports statuses and clears live text on reopen", () => {
    const onStatus = vi.fn();
    const { unmount } = render(<Probe id="s1" onStatus={onStatus} />);
    const es = FakeEventSource.last();
    act(() => {
      es.open();
      for (const line of fixtureLines("demo.jsonl").slice(0, 5)) {
        es.emit("flow", line);
      }
      es.emit("flow", mk("assistant.text_delta", { blockId: "b", text: "Hi" }));
    });
    expect(screen.getByTestId("seq")).toHaveTextContent("5");
    expect(screen.getByTestId("live")).toHaveTextContent("Hi");
    expect(onStatus).toHaveBeenCalledWith("working");
    act(() => es.open());
    expect(screen.getByTestId("live")).toBeEmptyDOMElement();
    unmount();
    expect(es.closed).toBe(true);
  });
});
