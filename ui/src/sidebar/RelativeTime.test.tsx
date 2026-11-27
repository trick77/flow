import { act, render, screen } from "@testing-library/react";

import { resources } from "../i18n/resources";
import { RelativeTime } from "./RelativeTime";

const en = resources.en.sidebar;

describe("RelativeTime", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("says how long ago, ticks once a minute, the exact time as tooltip", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-27T14:00:30Z"));
    const { unmount, container } = render(
      <>
        <RelativeTime at="2026-11-27T14:00:00Z" className="s-time" />
        <RelativeTime at="2026-11-27T12:00:00Z" />
      </>,
    );
    const [fresh, older] = [...container.querySelectorAll("time")];
    expect(screen.getByText("2h")).toBe(older);
    expect(fresh).toHaveTextContent(en.timeNow);
    expect(fresh).toHaveClass("s-time");
    expect(fresh).toHaveAttribute("datetime", "2026-11-27T14:00:00Z");
    expect(fresh.getAttribute("title")).toMatch(/^27\.11\.2026, \d\d:00$/);
    expect(older).toHaveTextContent("2h");

    // At the next full minute both update together.
    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(fresh).toHaveTextContent("1m");
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(fresh).toHaveTextContent("2m");
    unmount();
    // No timer left behind.
    expect(vi.getTimerCount()).toBe(0);
  });

  it("renders nothing for a time it cannot read", () => {
    const { container } = render(<RelativeTime at="not a date" />);
    expect(container).toBeEmptyDOMElement();
  });
});
