import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { PermissionRequested } from "../contract/events.gen";
import { resources } from "../i18n/resources";
import type { PermissionItem } from "./fold";
import { PermissionCard } from "./PermissionCard";

const en = resources.en;

const request: PermissionRequested = {
  requestId: "perm-1",
  tool: "Bash",
  kind: "command",
  command: "make check",
  cwd: "/home/flow/work/scener",
  mode: "accept_edits",
  canAlwaysAllow: true,
};

function item(over: Partial<PermissionItem> = {}): PermissionItem {
  return { type: "permission", key: "perm:perm-1", request, ...over };
}

describe("PermissionCard", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the command, where it runs and the mode", () => {
    render(<PermissionCard item={item()} sessionId="s1" provider="claude" />);
    expect(
      screen.getByText("Claude Code wants to run a command"),
    ).toBeInTheDocument();
    expect(screen.getByText(en.timeline.approvalNeeded)).toBeInTheDocument();
    expect(document.querySelector(".cmd")?.textContent).toBe("$ make check");
    expect(document.querySelector(".cwd")?.textContent).toBe(
      "in /home/flow/work/scener · mode Accept edits",
    );
  });

  it("shows a long command whole, wrapped, with the three choices in order", () => {
    const command = `bash -c 'for f in ${"a/".repeat(60)}x; do  echo "$f"; done'`;
    render(
      <PermissionCard
        item={item({ request: { ...request, command } })}
        sessionId="s1"
      />,
    );
    const pre = document.querySelector(".perm .cmd") as HTMLElement;
    expect(pre).toHaveClass("cmd-wrap");
    // Whitespace kept, nothing cut.
    expect(pre.textContent).toBe(`$ ${command}`);
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Allow once",
      "Allow for this session",
      "Deny",
    ]);
  });

  it.each([
    [en.timeline.allowOnce, "allow_once"],
    [en.timeline.allowAlways, "allow_always"],
    [en.timeline.deny, "deny"],
  ])("posts %s and waits for the resolution", async (label, decision) => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    render(<PermissionCard item={item()} sessionId="s 1" />);
    fireEvent.click(screen.getByRole("button", { name: label }));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/sessions/s%201/permissions/perm-1");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ decision });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    for (const b of screen.getAllByRole("button")) expect(b).toBeDisabled();
  });

  it("shows a failed answer and lets the owner try again", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "request_not_pending" }), {
        status: 409,
      }),
    );
    render(<PermissionCard item={item()} sessionId="s1" />);
    fireEvent.click(screen.getByRole("button", { name: en.timeline.deny }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.errors.request_not_pending,
    );
    expect(
      screen.getByRole("button", { name: en.timeline.allowOnce }),
    ).toBeEnabled();
  });

  it("offers Allow for this session only when the provider can remember a rule", () => {
    render(
      <PermissionCard
        item={item({
          request: {
            ...request,
            kind: "edit",
            command: undefined,
            path: "ui/src/App.tsx",
            cwd: undefined,
            canAlwaysAllow: false,
          },
        })}
        sessionId="s1"
        provider="opencode"
      />,
    );
    expect(
      screen.getByText("OpenCode wants to edit a file"),
    ).toBeInTheDocument();
    expect(document.querySelector(".cmd")?.textContent).toBe("ui/src/App.tsx");
    expect(document.querySelector(".cwd")?.textContent).toBe(
      "mode Accept edits",
    );
    expect(
      screen.queryByRole("button", { name: en.timeline.allowAlways }),
    ).toBeNull();
  });

  it("names an unknown agent and request kind neutrally", () => {
    render(
      <PermissionCard
        item={item({
          request: {
            ...request,
            kind: "teleport" as PermissionRequested["kind"],
            tool: "Teleport",
            command: undefined,
            mode: "warp" as PermissionRequested["mode"],
            cwd: undefined,
          },
        })}
        sessionId="s1"
      />,
    );
    expect(
      screen.getByText("The agent wants to use Teleport"),
    ).toBeInTheDocument();
    expect(document.querySelector(".cmd")).toBeNull();
    expect(document.querySelector(".cwd")).toBeNull();
  });

  it.each([
    ["allow_once", en.timeline.resolved.allow_once, false],
    ["allow_always", en.timeline.resolved.allow_always, false],
    ["deny", en.timeline.resolved.deny, true],
    ["expired", en.timeline.resolved.expired, true],
    ["future", en.timeline.resolved.expired, true],
  ] as const)("resolves %s to a compact row", (decision, label, denied) => {
    const { container } = render(
      <PermissionCard
        item={item({ decision: decision as PermissionItem["decision"] })}
        sessionId="s1"
      />,
    );
    const row = container.querySelector(".resolved") as HTMLElement;
    expect(row).toHaveTextContent(`${label} make check`);
    expect(row.classList.contains("denied")).toBe(denied);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
