import { fireEvent, render, screen, within } from "@testing-library/react";

import type { TypedEvent } from "../contract/events.gen";
import { resources } from "../i18n/resources";
import { mk } from "../test/flowEvents";
import { rawText } from "./Activity";
import { foldEvents } from "./fold";
import { emptyStream, reduceStream, type StreamState } from "./state";
import { Timeline } from "./Timeline";

const en = resources.en.timeline;

function show(events: TypedEvent[]) {
  const state = events.reduce<StreamState>(
    (acc, event) => reduceStream(acc, { type: "event", event }),
    emptyStream,
  );
  render(
    <Timeline sessionId="s1" folded={foldEvents(state)} working={false} />,
  );
  fireEvent.click(screen.getByRole("button", { name: /^Worked for/ }));
}

const t = "t-1";
const turn = (events: TypedEvent[]): TypedEvent[] => [
  mk("turn.started", { messageId: "m" }, { seq: 1, turn: t }),
  ...events,
  mk(
    "turn.ended",
    { outcome: "completed", durationMs: 2000 },
    { seq: 99, turn: t },
  ),
];

// Nothing an agent emits is invisible (docs/events.md, "Pass-through").
describe("generic rows", () => {
  it("shows a provider message nobody maps by its name, its content on demand", () => {
    show(
      turn([
        mk(
          "provider.passthrough",
          {
            provider: "claude",
            name: "system/workspace_indexed",
            raw: { type: "system", files: 1840 },
          },
          { seq: 2, turn: t },
        ),
        mk(
          "provider.passthrough",
          {
            provider: "claude",
            name: "system/large_report",
            raw: '{"type":"system","entries":[{"id":1,',
            rawTruncated: true,
          },
          { seq: 3, turn: t, parentTool: "toolu_p" },
        ),
      ]),
    );
    const r = screen.getByTestId("raw:2");
    expect(r).toHaveTextContent(`${en.passthrough}system/workspace_indexed`);
    const toggle = within(r).getByRole("button");
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(r.querySelector("pre")).toBeNull();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(r.querySelector("pre")?.textContent).toBe(
      JSON.stringify({ type: "system", files: 1840 }, null, 2),
    );
    expect(r).not.toHaveTextContent(en.rawTruncated);

    const cut = screen.getByTestId("raw:3");
    expect(cut).toHaveClass("nested");
    fireEvent.click(within(cut).getByRole("button"));
    expect(cut.querySelector("pre")?.textContent).toBe(
      '{"type":"system","entries":[{"id":1,',
    );
    expect(cut).toHaveTextContent(en.rawTruncated);
    fireEvent.click(within(cut).getByRole("button"));
    expect(cut.querySelector("pre")).toBeNull();
  });

  it("shows a tool nobody knows with its name, input and result", () => {
    show(
      turn([
        mk(
          "tool.started",
          {
            toolId: "u1",
            name: "Workbench",
            kind: "other",
            input: { action: "profile" },
          },
          { seq: 2, turn: t },
        ),
        mk(
          "tool.finished",
          { toolId: "u1", status: "ok", rawResult: { hot: ["a.go"] } },
          { seq: 3, turn: t },
        ),
        mk(
          "tool.started",
          {
            toolId: "u2",
            name: "Sketch",
            kind: "other",
            input: '{"shape":"ci',
            inputTruncated: true,
          },
          { seq: 4, turn: t },
        ),
        mk(
          "tool.finished",
          { toolId: "u2", status: "error", output: "no canvas" },
          { seq: 5, turn: t },
        ),
        // Known tools keep their plain row.
        mk(
          "tool.started",
          { toolId: "u3", name: "Read", kind: "read", path: "a.go", input: {} },
          { seq: 6, turn: t },
        ),
      ]),
    );
    const u1 = screen.getByTestId("tool:u1");
    expect(u1).toHaveTextContent(`${en.verbs.other}Workbench`);
    fireEvent.click(within(u1).getByRole("button"));
    const blocks = [...u1.querySelectorAll("pre")].map((p) => p.textContent);
    expect(blocks).toEqual([
      JSON.stringify({ action: "profile" }, null, 2),
      JSON.stringify({ hot: ["a.go"] }, null, 2),
    ]);
    expect(u1).toHaveTextContent(en.rawInput);
    expect(u1).toHaveTextContent(en.rawResult);

    const u2 = screen.getByTestId("tool:u2");
    expect(u2).toHaveTextContent(en.toolFailed);
    fireEvent.click(within(u2).getByRole("button"));
    expect([...u2.querySelectorAll("pre")].map((p) => p.textContent)).toEqual([
      '{"shape":"ci',
      "no canvas",
    ]);
    expect(u2).toHaveTextContent(en.rawTruncated);

    expect(
      within(screen.getByTestId("tool:u3")).queryByRole("button"),
    ).toBeNull();
  });

  it("writes raw content as the owner reads it", () => {
    expect(rawText("as is")).toBe("as is");
    expect(rawText(null)).toBe("null");
    expect(rawText(undefined)).toBe("");
  });
});

// Every notice code an adapter or flowd emits has a sentence (docs/events.md,
// "Notice codes"): none shows as a raw code.
describe("notice texts", () => {
  const codes = [
    "context_compacted",
    "context_compaction_failed",
    "rate_limited",
    "rate_limit_warning",
    "api_retry",
    "auth_required",
    "provider_error",
    "mode_change_failed",
    "model_change_failed",
    "draft_too_large",
    "request_too_large",
    "sidecar_translate_failed",
    "sidecar_bad_command",
    "sidecar_command_skipped",
    "sidecar_shutting_down",
    "sidecar_busy",
    "sidecar_reply_not_pending",
    "github_token_expired",
  ];
  it.each(codes)("%s", (code) => {
    const state = reduceStream(emptyStream, {
      type: "event",
      event: mk(
        "notice",
        { code, level: "warning", params: { reason: "withdrawn" } },
        { seq: 1 },
      ),
    });
    render(
      <Timeline sessionId="s1" folded={foldEvents(state)} working={false} />,
    );
    const text = (en.notices as Record<string, string>)[code];
    expect(text).toBeTruthy();
    expect(screen.getByText(text)).toBeInTheDocument();
  });
});
