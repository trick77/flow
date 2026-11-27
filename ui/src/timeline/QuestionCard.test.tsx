import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { QuestionAsked } from "../contract/events.gen";
import { resources } from "../i18n/resources";
import type { QuestionItem } from "./fold";
import { answerSummary, QuestionCard } from "./QuestionCard";

const en = resources.en;
const sep = en.timeline.separator;

const one: QuestionAsked = {
  requestId: "q-1",
  questions: [
    {
      id: "q0",
      header: "Cache key",
      text: "Should a green run still count after the gate config changed?",
      multiSelect: false,
      options: [
        {
          id: "o0",
          label: "Key on commit SHA + gate config hash",
          description: "Recommended.",
        },
        { id: "o1", label: "Key on commit SHA only" },
      ],
    },
  ],
};

const two: QuestionAsked = {
  requestId: "q-2",
  questions: [
    one.questions[0],
    {
      id: "q1",
      text: "Which checks may it skip?",
      multiSelect: true,
      options: [
        { id: "lint", label: "Lint" },
        { id: "test", label: "Tests" },
        { id: "build", label: "Build" },
      ],
    },
  ],
};

function item(
  request: QuestionAsked,
  over: Partial<QuestionItem> = {},
): QuestionItem {
  return {
    type: "question",
    key: `question:${request.requestId}`,
    request,
    open: true,
    ...over,
  };
}

describe("QuestionCard", () => {
  const fetchMock = vi.fn<typeof fetch>();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("asks one question with its options and sends the choice", async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<QuestionCard item={item(one)} sessionId="s1" provider="claude" />);
    expect(screen.getByText(one.questions[0].text)).toBeInTheDocument();
    expect(screen.getByText(en.timeline.question)).toHaveClass("pill-cold");
    const group = screen.getByRole("radiogroup", { name: "Cache key" });
    expect(group).toBeInTheDocument();
    expect(screen.getByText("Recommended.")).toBeInTheDocument();
    const send = screen.getByRole("button", { name: en.timeline.sendAnswer });
    expect(send).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: /SHA only/ }));
    fireEvent.click(screen.getByRole("radio", { name: /config hash/ }));
    expect(screen.getByRole("radio", { name: /SHA only/ })).not.toBeChecked();
    fireEvent.click(send);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/s1/questions/q-1");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      answers: [{ questionId: "q0", optionIds: ["o0"] }],
    });
    // Waits for question.resolved; no second send.
    await waitFor(() => expect(send).toBeDisabled());
  });

  it("takes several choices and a free answer per question", () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<QuestionCard item={item(two)} sessionId="s1" provider="claude" />);
    expect(
      screen.getByText("2 questions from Claude Code"),
    ).toBeInTheDocument();
    expect(screen.getByText("Which checks may it skip?")).toBeInTheDocument();
    const multi = screen.getByRole("group", {
      name: "Which checks may it skip?",
    });
    expect(multi).toBeInTheDocument();
    fireEvent.click(screen.getByRole("checkbox", { name: "Lint" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Build" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Lint" }));
    const send = screen.getByRole("button", { name: en.timeline.sendAnswer });
    // The first question still has no answer.
    expect(send).toBeDisabled();
    fireEvent.change(
      screen.getByRole("textbox", {
        name: `Cache key: ${en.timeline.questionOther}`,
      }),
      { target: { value: " tree hash " } },
    );
    fireEvent.click(send);
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      answers: [
        { questionId: "q0", optionIds: [], text: "tree hash" },
        { questionId: "q1", optionIds: ["build"] },
      ],
    });
  });

  it("gives each card's radio group its own name", () => {
    render(
      <>
        <QuestionCard item={item(one)} sessionId="s1" />
        <QuestionCard
          item={item({ ...one, requestId: "q-9" })}
          sessionId="s1"
        />
      </>,
    );
    const names = screen
      .getAllByRole("radio")
      .map((r) => r.getAttribute("name"));
    expect(new Set(names).size).toBe(2);
  });

  it("hands over to the composer", () => {
    const onAnswerInComposer = vi.fn();
    render(
      <QuestionCard
        item={item(one)}
        sessionId="s1"
        onAnswerInComposer={onAnswerInComposer}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.answerInComposer }),
    );
    expect(onAnswerInComposer).toHaveBeenCalled();
  });

  it("says why a reply failed and lets it be sent again", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "not_implemented" }), {
        status: 501,
      }),
    );
    render(<QuestionCard item={item(one)} sessionId="s1" />);
    fireEvent.click(screen.getByRole("radio", { name: /SHA only/ }));
    fireEvent.click(
      screen.getByRole("button", { name: en.timeline.sendAnswer }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.not_implemented,
    );
    expect(
      screen.getByRole("button", { name: en.timeline.sendAnswer }),
    ).toBeEnabled();
  });

  it("shrinks to the answer once resolved", () => {
    const resolved = item(one, {
      open: false,
      resolved: {
        requestId: "q-1",
        outcome: "answered",
        answers: [{ questionId: "q0", optionIds: ["o1"], text: "for now" }],
      },
    });
    render(<QuestionCard item={resolved} sessionId="s1" />);
    expect(document.querySelector(".resolved")?.textContent).toBe(
      `${en.timeline.answered}${sep}Key on commit SHA only, for now`,
    );
    expect(screen.queryByRole("radio")).toBeNull();
  });

  it("names a composer answer and an expired question", () => {
    const { rerender } = render(
      <QuestionCard
        item={item(one, {
          open: false,
          resolved: { requestId: "q-1", outcome: "answered", text: "tree" },
        })}
        sessionId="s1"
      />,
    );
    expect(document.querySelector(".resolved")?.textContent).toBe(
      `${en.timeline.answeredInComposer}${sep}tree`,
    );
    rerender(
      <QuestionCard
        item={item(one, {
          open: false,
          resolved: { requestId: "q-1", outcome: "expired" },
        })}
        sessionId="s1"
      />,
    );
    expect(document.querySelector(".resolved.denied")).toHaveTextContent(
      en.timeline.questionExpired,
    );
    // A turn that ended without an answer reads as expired too.
    rerender(<QuestionCard item={item(one, { open: false })} sessionId="s1" />);
    expect(document.querySelector(".resolved.denied")).toHaveTextContent(
      en.timeline.questionExpired,
    );
  });

  it("summarises unknown ids as they are", () => {
    expect(
      answerSummary(
        item(one, {
          resolved: {
            requestId: "q-1",
            outcome: "answered",
            answers: [
              { questionId: "zz", optionIds: ["x"] },
              { questionId: "q0", optionIds: [] },
            ],
          },
        }),
      ),
    ).toBe("x");
    expect(answerSummary(item(one))).toBe("");
    expect(
      answerSummary(
        item(one, { resolved: { requestId: "q-1", outcome: "answered" } }),
      ),
    ).toBe("");
  });
});
