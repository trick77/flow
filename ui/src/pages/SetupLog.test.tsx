import { fireEvent, render, screen } from "@testing-library/react";

import type { Project } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { ProjectsPage } from "./ProjectsPage";
import { SetupLog } from "./SetupLog";

const en = resources.en.projects;

function serve(answers: { status: number; body: unknown }[]) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      calls.push(String(input));
      const a = answers.shift();
      if (!a) throw new Error("no answer");
      return new Response(JSON.stringify(a.body), { status: a.status });
    }),
  );
  return calls;
}

const lines = (from: number, n: number) =>
  Array.from({ length: n }, (_, i) => ({
    n: from + i,
    text: `line ${from + i}`,
  }));

afterEach(() => vi.unstubAllGlobals());

describe("SetupLog", () => {
  it("reads every page and shows the lines", async () => {
    const calls = serve([
      { status: 200, body: { state: "failed", lines: lines(1, 1000) } },
      { status: 200, body: { state: "failed", lines: lines(1001, 2) } },
    ]);
    render(<SetupLog projectId="p 1" />);
    expect(screen.getByRole("status")).toHaveTextContent(en.setupLogLoading);
    const log = await screen.findByLabelText(en.setupLog);
    expect(log.textContent?.split("\n")).toHaveLength(1002);
    expect(log.textContent).toMatch(/line 1002$/);
    expect(calls).toEqual([
      "/api/projects/p%201/sandbox/setup-log",
      "/api/projects/p%201/sandbox/setup-log?after=1000",
    ]);
  });

  it("says an empty log and a refusal", async () => {
    serve([{ status: 200, body: { state: "failed", lines: [] } }]);
    const { unmount } = render(<SetupLog projectId="p1" />);
    expect(await screen.findByText(en.setupLogEmpty)).toBeInTheDocument();
    unmount();
    serve([{ status: 503, body: { code: "sandbox_unavailable" } }]);
    render(<SetupLog projectId="p1" />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      `${en.setupLogFailed} ${resources.en.errors.sandbox_unavailable}`,
    );
  });

  it("is offered on the Projects page only for a failed setup script", async () => {
    const p = (errorCode?: "sandbox_setup_failed" | "internal"): Project => ({
      id: errorCode ?? "ok",
      name: errorCode ?? "ok",
      repos: [],
      sandbox: { state: "failed", errorCode },
      createdAt: "2026-11-26T10:00:00Z",
    });
    serve([{ status: 200, body: { state: "failed", lines: lines(1, 1) } }]);
    render(
      <ProjectsPage
        projects={[p("sandbox_setup_failed"), p("internal")]}
        loaded
        error={null}
        onEvent={() => {}}
        nav={null}
      />,
    );
    expect(
      screen.getByText(resources.en.errors.sandbox_setup_failed),
    ).toBeInTheDocument();
    const show = screen.getAllByRole("button", { name: en.setupLog });
    expect(show).toHaveLength(1);
    fireEvent.click(show[0]);
    expect(await screen.findByLabelText(en.setupLog)).toHaveTextContent(
      "line 1",
    );
    const hide = screen.getByRole("button", { name: en.hideSetupLog });
    expect(hide).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(hide);
    expect(screen.queryByLabelText(en.setupLog)).toBeNull();
  });
});
