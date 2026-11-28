import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";

import type { Diff, Worktree } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { STORAGE_KEYS } from "../shell/storage";
import { ReviewPanel } from "./ReviewPanel";

const en = resources.en.panel;

const wt = (repoId: string, repo: string): Worktree => ({
  repoId,
  repo,
  branch: "feat/x",
  path: `/w/${repo}`,
  baseRef: "origin/main",
});

const diff: Diff = {
  repo: "org/app",
  baseRef: "origin/main",
  baseSha: "abc",
  truncated: true,
  files: [
    {
      path: "src/a.ts",
      change: "modified",
      added: 1,
      removed: 1,
      hunks: [
        {
          header: "@@ -3,2 +3,2 @@",
          oldStart: 3,
          oldLines: 2,
          newStart: 3,
          newLines: 2,
          lines: [" keep", "-old", "+new"],
        },
      ],
    },
    {
      path: "src/b.ts",
      oldPath: "src/old-b.ts",
      change: "renamed",
      added: 0,
      removed: 0,
      hunks: [],
    },
    {
      path: "logo.png",
      change: "added",
      added: 0,
      removed: 0,
      binary: true,
      hunks: [],
    },
    {
      path: "gone.ts",
      change: "deleted",
      added: 0,
      removed: 9,
      truncated: true,
      hunks: [],
    },
  ],
};

type Answer = { status: number; body: unknown } | Promise<never>;

function serve(answers: Record<string, Answer[]>) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push(`${init?.method ?? "GET"} ${url}`);
      const next = answers[url]?.shift();
      if (!next) throw new Error("no answer for " + url);
      const a = await next;
      return new Response(JSON.stringify(a.body), { status: a.status });
    }),
  );
  return calls;
}

const URL1 = "/api/sessions/s1/repos/r1/diff";
const URL2 = "/api/sessions/s1/repos/r2/diff";

function renderPanel(over: Partial<Parameters<typeof ReviewPanel>[0]> = {}) {
  const props = {
    sessionId: "s1",
    projectId: "p1",
    branch: "feat/x",
    worktrees: [wt("r1", "org/app"), wt("r2", "org/lib")],
    rev: 0,
    onRepo: vi.fn(),
    onRefresh: vi.fn(),
    onClose: vi.fn(),
    ...over,
  };
  const view = render(<ReviewPanel {...props} />);
  return { ...view, props };
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

describe("ReviewPanel", () => {
  it("shows the files, counts, numbered hunks and the API's markers", async () => {
    serve({ [URL1]: [{ status: 200, body: diff }] });
    renderPanel();
    expect(screen.getByRole("status")).toHaveTextContent(en.loading);
    const list = await screen.findByRole("list", { name: en.filesLabel });
    expect(within(list).getAllByRole("button")).toHaveLength(4);
    expect(
      screen.getByText("feat/x vs origin/main · read-only"),
    ).toBeInTheDocument();
    // Totals and per file, read out as sentences.
    expect(
      screen.getAllByLabelText("1 lines added, 10 removed")[0],
    ).toBeInTheDocument();
    const a = screen.getByRole("region", { name: "src/a.ts" });
    const rows = within(a)
      .getAllByRole("row")
      .map((r) => r.textContent);
    expect(rows).toEqual(["@@ -3,2 +3,2 @@", "33keep", "4−old", "4+new"]);
    expect(screen.getByText("src/old-b.ts → src/b.ts")).toBeInTheDocument();
    expect(screen.getByText(en.changeRenamed)).toBeInTheDocument();
    expect(screen.getByText(en.changeAdded)).toBeInTheDocument();
    expect(screen.getByText(en.changeDeleted)).toHaveClass("pill-del");
    expect(screen.getByText(en.binary)).toBeInTheDocument();
    expect(screen.getByText(en.fileTruncated)).toBeInTheDocument();
    expect(screen.getByText(en.diffTruncated)).toBeInTheDocument();

    // A file in the list jumps to its diff.
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    fireEvent.click(within(list).getByText("gone.ts"));
    expect(scroll).toHaveBeenCalledWith({ block: "start", behavior: "smooth" });
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    fireEvent.click(within(list).getByText("gone.ts"));
  });

  it("switches repositories with the tabs and arrow keys, empty diff said so", async () => {
    serve({
      [URL1]: [{ status: 200, body: diff }],
      [URL2]: [
        {
          status: 200,
          body: {
            repo: "org/lib",
            baseRef: "origin/main",
            baseSha: "x",
            files: [],
          },
        },
      ],
    });
    const { props, rerender } = renderPanel();
    await screen.findByRole("list", { name: en.filesLabel });
    const group = screen.getByRole("radiogroup", { name: en.repository });
    const [app, lib] = within(group).getAllByRole("radio");
    expect(app).toHaveAttribute("aria-checked", "true");
    expect(lib).toHaveAttribute("tabindex", "-1");
    fireEvent.click(lib);
    expect(props.onRepo).toHaveBeenCalledWith("r2");
    fireEvent.keyDown(app, { key: "ArrowRight" });
    expect(props.onRepo).toHaveBeenLastCalledWith("r2");
    fireEvent.keyDown(app, { key: "ArrowLeft" });
    expect(props.onRepo).toHaveBeenLastCalledWith("r2");
    fireEvent.keyDown(app, { key: "a" });
    rerender(<ReviewPanel {...props} repoId="r2" />);
    expect(
      await screen.findByText("No changes against origin/main yet."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: en.filesLabel })).toBeNull();
  });

  it("reads again on a new rev and drops an older answer", async () => {
    let release: (a: { status: number; body: unknown }) => void = () => {};
    const slow = new Promise<{ status: number; body: unknown }>((r) => {
      release = r;
    });
    const calls = serve({
      [URL1]: [
        slow as unknown as Answer,
        { status: 200, body: { ...diff, files: [], truncated: false } },
      ],
    });
    const { props, rerender } = renderPanel();
    fireEvent.click(screen.getByRole("button", { name: en.refresh }));
    expect(props.onRefresh).toHaveBeenCalled();
    rerender(<ReviewPanel {...props} rev={1} />);
    expect(
      await screen.findByText("No changes against origin/main yet."),
    ).toBeInTheDocument();
    await act(async () => release({ status: 200, body: diff }));
    expect(screen.queryByRole("list", { name: en.filesLabel })).toBeNull();
    expect(calls).toEqual([`GET ${URL1}`, `GET ${URL1}`]);
  });

  it("says a session without worktrees has none yet, and waits for them", () => {
    serve({});
    const { rerender, props } = renderPanel({ worktrees: undefined });
    expect(screen.getByRole("status")).toHaveTextContent(en.loading);
    rerender(<ReviewPanel {...props} worktrees={[]} />);
    expect(screen.getByText(en.noWorktree)).toBeInTheDocument();
  });

  it("offers Start when the sandbox does not run, and says a refusal", async () => {
    const calls = serve({
      [URL1]: [{ status: 503, body: { code: "sandbox_unavailable" } }],
      "/api/projects/p1/sandbox/start": [
        { status: 409, body: { code: "sandbox_busy" } },
        { status: 202, body: { state: "setup" } },
      ],
    });
    const { props, rerender } = renderPanel({ sandboxState: "stopped" });
    const start = await screen.findByRole("button", { name: en.startSandbox });
    expect(screen.getByText(en.sandboxDown)).toBeInTheDocument();
    fireEvent.click(start);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      resources.en.errors.sandbox_busy,
    );
    fireEvent.click(screen.getByRole("button", { name: en.startSandbox }));
    expect(screen.getByText(en.sandboxStarting)).toBeInTheDocument();
    expect(calls).toContain("POST /api/projects/p1/sandbox/start");
    // The start failed later: the new state offers Start again.
    rerender(<ReviewPanel {...props} sandboxState="failed" />);
    expect(
      screen.getByRole("button", { name: en.startSandbox }),
    ).toBeInTheDocument();
  });

  it("offers a retry when the sandbox runs but cannot answer, and for other errors", async () => {
    serve({ [URL1]: [{ status: 503, body: { code: "sandbox_unavailable" } }] });
    const { props, unmount } = renderPanel({ sandboxState: "warm" });
    fireEvent.click(await screen.findByRole("button", { name: en.retry }));
    expect(props.onRefresh).toHaveBeenCalledTimes(1);
    unmount();

    const second = renderPanel({ worktreesError: "internal" });
    expect(screen.getByRole("alert")).toHaveTextContent(
      resources.en.errors.internal,
    );
    fireEvent.click(screen.getByRole("button", { name: en.retry }));
    expect(second.props.onRefresh).toHaveBeenCalled();
  });

  it("shows starting while the sandbox sets up", async () => {
    serve({ [URL1]: [{ status: 503, body: { code: "sandbox_unavailable" } }] });
    renderPanel({ sandboxState: "setup", sandboxBusy: true });
    expect(await screen.findByText(en.sandboxStarting)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.startSandbox })).toBeNull();
  });

  it("offers a start for a setup left behind, none during a stop", async () => {
    serve({ [URL1]: [{ status: 503, body: { code: "sandbox_unavailable" } }] });
    const view = renderPanel({ sandboxState: "setup" });
    expect(
      await screen.findByRole("button", { name: en.startSandbox }),
    ).toBeInTheDocument();
    view.unmount();
    serve({ [URL1]: [{ status: 503, body: { code: "sandbox_unavailable" } }] });
    renderPanel({ sandboxState: "stopping", sandboxBusy: true });
    expect(await screen.findByText(en.sandboxDown)).toBeInTheDocument();
    expect(screen.queryByText(en.sandboxStarting)).toBeNull();
    expect(screen.queryByRole("button", { name: en.startSandbox })).toBeNull();
    expect(screen.queryByRole("button", { name: en.retry })).toBeNull();
  });

  it("resizes with the keyboard and a drag, and remembers the width", async () => {
    serve({ [URL1]: [{ status: 200, body: diff }] });
    window.localStorage.setItem(STORAGE_KEYS.panelWidth, "400");
    const { container } = renderPanel();
    await screen.findByRole("list", { name: en.filesLabel });
    const panel = container.querySelector(".rpanel") as HTMLElement;
    const handle = screen.getByRole("separator", { name: en.resize });
    expect(handle).toHaveAttribute("aria-valuenow", "400");
    expect(panel.style.getPropertyValue("--panel-w")).toBe("400px");

    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(handle).toHaveAttribute("aria-valuenow", "416");
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(handle).toHaveAttribute("aria-valuenow", "384");
    fireEvent.keyDown(handle, { key: "Home" });
    expect(handle).toHaveAttribute("aria-valuenow", "320");
    fireEvent.keyDown(handle, { key: "End" });
    const max = handle.getAttribute("aria-valuemax");
    expect(handle).toHaveAttribute("aria-valuenow", max);
    fireEvent.keyDown(handle, { key: "Tab" });

    fireEvent.pointerDown(handle, { button: 2, clientX: 500 });
    fireEvent.pointerDown(handle, { button: 0, clientX: 500, pointerId: 1 });
    fireEvent.keyDown(handle, { key: "Home" });
    fireEvent.pointerDown(handle, { button: 0, clientX: 500, pointerId: 1 });
    fireEvent.pointerMove(handle, { clientX: 450 });
    expect(handle).toHaveAttribute("aria-valuenow", "370");
    fireEvent.pointerUp(handle);
    expect(window.localStorage.getItem(STORAGE_KEYS.panelWidth)).toBe("370");
    fireEvent.pointerMove(handle, { clientX: 300 });
    expect(handle).toHaveAttribute("aria-valuenow", "370");
    fireEvent.pointerUp(handle);
  });

  it("covers the session when narrow (no handle) and closes on Escape while it overlays", async () => {
    serve({ [URL1]: [{ status: 200, body: diff }] });
    const { props, rerender } = renderPanel({ narrow: true, overlay: true });
    await screen.findByRole("list", { name: en.filesLabel });
    expect(screen.queryByRole("separator")).toBeNull();
    expect(screen.getByRole("button", { name: en.close })).toHaveFocus();
    fireEvent.keyDown(screen.getByRole("button", { name: en.close }), {
      key: "Escape",
    });
    expect(props.onClose).toHaveBeenCalledTimes(1);
    rerender(<ReviewPanel {...props} overlay={false} narrow={false} />);
    fireEvent.keyDown(screen.getByRole("button", { name: en.close }), {
      key: "Escape",
    });
    expect(props.onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: en.close }));
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(2));
  });
});
