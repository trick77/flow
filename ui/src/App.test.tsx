import { render, screen } from "@testing-library/react";

import { App } from "./App";
import { resources } from "./i18n/resources";

describe("App", () => {
  it("shows the wordmark and the empty state from the resources", () => {
    render(<App />);
    expect(
      screen.getByRole("heading", {
        level: 1,
        name: resources.en.common.appName,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText(resources.en.shell.empty)).toBeInTheDocument();
    expect(screen.getByText(resources.en.shell.emptyHint)).toBeInTheDocument();
  });
});
