import { act, fireEvent, render, screen, within } from "@testing-library/react";

import type { GitHubRepo, Project } from "../api/types.gen";
import { resources } from "../i18n/resources";
import { connections, json, routeFetch } from "../test/routeFetch";
import { AddProject } from "./AddProject";
import { forgetDeviceFlow } from "./useDeviceFlow";

const en = resources.en.projects;
const settings = resources.en.settings;
const errors = resources.en.errors;

const repo = (fullName: string, locked = false): GitHubRepo => ({
  fullName,
  defaultBranch: "main",
  private: locked,
});

const created: Project = {
  id: "p9",
  name: "x",
  repos: [],
  sandbox: { state: "setup" },
  createdAt: "2026-11-28T10:00:00Z",
};

const flush = () => act(async () => {});

async function renderForm() {
  const onCreated = vi.fn();
  const onClose = vi.fn();
  render(<AddProject onClose={onClose} onCreated={onCreated} />);
  await flush();
  return { onCreated, onClose };
}

const nameField = () => screen.getByLabelText(en.name);
const byName = (label = en.byName) => screen.getByLabelText(label);
const submitBtn = () => screen.getByRole("button", { name: en.create });

function addByName(text: string, label = en.byName) {
  fireEvent.change(byName(label), { target: { value: text } });
  fireEvent.keyDown(byName(label), { key: "Enter" });
}

const chips = () =>
  within(screen.getByRole("list", { name: en.repoChosen }))
    .getAllByRole("listitem")
    .map((li) => li.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  forgetDeviceFlow();
});

describe("AddProject without GitHub on the server", () => {
  it("says so, offers no connect, and adds public repositories by name", async () => {
    const calls = routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: false,
      }),
      "POST /api/projects": json(201, created),
    });
    const { onCreated } = await renderForm();
    expect(screen.getByText(en.ghUnavailable)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.ghConnect })).toBeNull();
    expect(submitBtn()).toBeDisabled();
    expect(screen.getByText(en.needRepos)).toBeInTheDocument();

    addByName("cli");
    expect(screen.getByRole("alert")).toHaveTextContent(en.byNameInvalid);
    expect(byName()).toHaveAttribute("aria-invalid", "true");
    addByName("https://github.com/cli/cli.git");
    expect(chips()).toEqual(["cli/cli"]);
    expect(nameField()).toHaveValue("cli");
    addByName("CLI/cli");
    expect(screen.getByRole("alert")).toHaveTextContent(en.byNameTwice);
    fireEvent.change(byName(), { target: { value: "cli/go-gh" } });
    fireEvent.click(screen.getByRole("button", { name: en.byNameAdd }));
    expect(chips()).toEqual(["cli/cli", "cli/go-gh"]);

    await act(async () => {
      fireEvent.click(submitBtn());
    });
    expect(calls[calls.length - 1]).toEqual({
      method: "POST",
      url: "/api/projects",
      body: { name: "cli", repos: ["cli/cli", "cli/go-gh"] },
    });
    expect(onCreated).toHaveBeenCalledWith(created);
  });

  it("reads a 501 for the connections as no GitHub", async () => {
    routeFetch({
      "GET /api/connections": json(501, { code: "not_implemented" }),
    });
    await renderForm();
    expect(screen.getByText(en.ghUnavailable)).toBeInTheDocument();
  });

  it("still adds by name when the connections cannot be read", async () => {
    routeFetch({ "GET /api/connections": json(500, { code: "internal" }) });
    await renderForm();
    expect(screen.queryByText(en.ghUnavailable)).toBeNull();
    expect(byName()).toBeInTheDocument();
  });
});

describe("the name", () => {
  it("follows the first repository until typed, again once cleared", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: false,
      }),
    });
    await renderForm();
    expect(nameField()).toHaveValue("");
    expect(nameField()).toHaveAccessibleDescription(en.nameHint);
    addByName("octo/web-app");
    expect(nameField()).toHaveValue("web-app");
    fireEvent.change(nameField(), { target: { value: "shop" } });
    addByName("octo/other");
    expect(nameField()).toHaveValue("shop");
    fireEvent.click(
      screen.getByRole("button", {
        name: en.repoRemove.replace("{{name}}", "octo/web-app"),
      }),
    );
    expect(nameField()).toHaveValue("shop");
    // Emptied, it stays empty while focused: another name can be typed.
    fireEvent.change(nameField(), { target: { value: "" } });
    expect(nameField()).toHaveValue("");
    fireEvent.change(nameField(), { target: { value: "t" } });
    expect(nameField()).toHaveValue("t");
    // Left empty, it follows again.
    fireEvent.change(nameField(), { target: { value: "" } });
    fireEvent.blur(nameField());
    expect(nameField()).toHaveValue("other");
    fireEvent.change(nameField(), { target: { value: "   " } });
    expect(submitBtn()).toBeDisabled();
    expect(screen.getByText(en.needName)).toBeInTheDocument();
  });
});

describe("AddProject with GitHub not connected", () => {
  it("connects right in the form and keeps what was chosen", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: true,
      }),
      "POST /api/connections/github/device": json(200, {
        userCode: "WDJB-MJHT",
        verificationUri: "https://github.com/login/device",
        expiresAt: "2026-11-28T10:15:00Z",
        expiresInSeconds: 900,
        intervalSeconds: 1,
      }),
      "POST /api/connections/github/device/poll": [
        json(200, { state: "connected", available: true, login: "octo" }),
      ],
      "GET /api/github/repos": json(200, { items: [repo("octo/app", true)] }),
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await renderForm();
    expect(screen.getByText(en.ghOffer)).toBeInTheDocument();
    addByName("cli/cli", en.byNameOr);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.ghConnect }));
    });
    expect(screen.getByText("WDJB-MJHT")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(
      screen.getByText(settings.deviceConnectedAs.replace("{{login}}", "octo")),
    ).toBeInTheDocument();
    expect(screen.queryByText(en.ghOffer)).toBeNull();
    expect(chips()).toEqual(["cli/cli"]);
    expect(
      screen.getByRole("button", { name: /Add a repository/ }),
    ).toBeVisible();
  });

  it("says an expired connection and offers to connect again", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "expired",
        available: true,
      }),
      "POST /api/connections/github/device": json(502, { code: "upstream" }),
    });
    await renderForm();
    expect(screen.getByText(en.ghExpired)).toBeInTheDocument();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.ghConnectAgain }));
    });
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
    expect(
      screen.getByRole("button", { name: en.ghTryAgain }),
    ).toBeInTheDocument();
  });

  it("drops the offer when the server turns out to have no GitHub", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: true,
      }),
      "POST /api/connections/github/device": json(501, {
        code: "not_implemented",
      }),
    });
    await renderForm();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.ghConnect }));
    });
    expect(screen.getByText(en.ghUnavailable)).toBeInTheDocument();
  });
});

describe("AddProject with GitHub connected", () => {
  it("picks from the list and sends owner/name only", async () => {
    const calls = routeFetch({
      "GET /api/connections": connections({
        state: "connected",
        available: true,
      }),
      "GET /api/github/repos": json(200, {
        items: [repo("octo/app", true), repo("octo/site")],
      }),
      "POST /api/projects": json(201, created),
    });
    await renderForm();
    expect(byNameQuery()).toBeNull();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Choose repositories/ }),
      );
    });
    fireEvent.click(screen.getByRole("option", { name: /octo\/site/ }));
    fireEvent.click(screen.getByRole("option", { name: /octo\/app/ }));
    expect(chips()).toEqual(["octo/site", "octo/app"]);
    expect(nameField()).toHaveValue("site");
    await act(async () => {
      fireEvent.click(submitBtn());
    });
    expect(calls[calls.length - 1].body).toEqual({
      name: "site",
      repos: ["octo/site", "octo/app"],
    });
  });

  it("puts each refusal where it belongs", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "connected",
        available: true,
      }),
      "GET /api/github/repos": json(200, { items: [repo("octo/app")] }),
      "POST /api/projects": [
        json(409, { code: "conflict", field: "name" }),
        json(400, { code: "invalid_request", field: "name" }),
        json(400, { code: "invalid_request", field: "repos" }),
        json(502, { code: "upstream" }),
        json(409, { code: "github_not_connected", field: "repos" }),
      ],
    });
    await renderForm();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Choose repositories/ }),
      );
    });
    fireEvent.click(screen.getByRole("option", { name: /octo\/app/ }));
    const submit = () =>
      act(async () => {
        fireEvent.click(submitBtn());
      });
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(en.nameTaken);
    expect(nameField()).toHaveAttribute("aria-invalid", "true");
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(en.invalidName);
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(en.invalidRepos);
    expect(
      screen.getByRole("group", { name: en.repos }),
    ).toHaveAccessibleDescription(en.invalidRepos);
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
    await submit();
    // Not found as a public one, and no connection after all: connect.
    expect(screen.getByText(en.notFoundPublic)).toBeInTheDocument();
    expect(screen.getByText(en.ghExpired)).toBeInTheDocument();
  });

  it("goes back to connecting when the list says the connection is gone", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "connected",
        available: true,
      }),
      "GET /api/github/repos": json(409, { code: "github_not_connected" }),
    });
    await renderForm();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Choose repositories/ }),
      );
    });
    expect(screen.getByText(en.ghExpired)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: en.ghConnectAgain }),
    ).toBeInTheDocument();
  });

  it("offers adding by name while the list cannot be read", async () => {
    const calls = routeFetch({
      "GET /api/connections": connections({
        state: "connected",
        available: true,
      }),
      "GET /api/github/repos": json(502, { code: "upstream" }),
      "POST /api/projects": json(201, created),
    });
    await renderForm();
    expect(byNameQuery()).toBeNull();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Choose repositories/ }),
      );
    });
    expect(screen.getByRole("alert")).toHaveTextContent(errors.upstream);
    addByName("cli/cli", en.byNameOr);
    expect(chips()).toEqual(["cli/cli"]);
    await act(async () => {
      fireEvent.click(submitBtn());
    });
    expect(calls[calls.length - 1].body).toEqual({
      name: "cli",
      repos: ["cli/cli"],
    });
  });

  it("clears a refusal of the repositories once one is removed", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "connected",
        available: true,
      }),
      "GET /api/github/repos": json(200, {
        items: [repo("octo/app"), repo("octo/gone")],
      }),
      "POST /api/projects": json(400, {
        code: "invalid_request",
        field: "repos",
      }),
    });
    await renderForm();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Choose repositories/ }),
      );
    });
    fireEvent.click(screen.getByRole("option", { name: /octo\/app/ }));
    fireEvent.click(screen.getByRole("option", { name: /octo\/gone/ }));
    await act(async () => {
      fireEvent.click(submitBtn());
    });
    expect(screen.getByText(en.invalidRepos)).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", {
        name: en.repoRemove.replace("{{name}}", "octo/gone"),
      }),
    );
    expect(screen.queryByText(en.invalidRepos)).toBeNull();
  });

  it("drops the connected line when a connection made here is lost", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: true,
      }),
      "POST /api/connections/github/device": json(200, {
        userCode: "WDJB-MJHT",
        verificationUri: "https://github.com/login/device",
        expiresAt: "2026-11-28T10:15:00Z",
        expiresInSeconds: 900,
        intervalSeconds: 1,
      }),
      "POST /api/connections/github/device/poll": json(200, {
        state: "connected",
        available: true,
        login: "octo",
      }),
      "GET /api/github/repos": json(409, { code: "github_not_connected" }),
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await renderForm();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: en.ghConnect }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    const connected = settings.deviceConnectedAs.replace("{{login}}", "octo");
    expect(screen.getByText(connected)).toBeInTheDocument();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: /Choose repositories/ }),
      );
    });
    expect(screen.getByText(en.ghExpired)).toBeInTheDocument();
    expect(screen.queryByText(connected)).toBeNull();
  });

  it("refuses by name one the server would refuse, saying why", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: false,
      }),
    });
    await renderForm();
    addByName("octo/.github");
    expect(screen.getByRole("alert")).toHaveTextContent(en.repoBadDir);
    addByName("octo/app");
    addByName("fork/App");
    expect(screen.getByRole("alert")).toHaveTextContent(
      en.repoSameDir.replace("{{name}}", "octo/app"),
    );
    expect(chips()).toEqual(["octo/app"]);
  });
});

describe("AddProject closing", () => {
  it("closes by its button and by cancel", async () => {
    routeFetch({
      "GET /api/connections": connections({
        state: "disconnected",
        available: false,
      }),
    });
    const { onClose } = await renderForm();
    fireEvent.click(screen.getByRole("button", { name: en.close }));
    fireEvent.click(screen.getByRole("button", { name: en.cancel }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});

function byNameQuery() {
  return screen.queryByLabelText(en.byName);
}
