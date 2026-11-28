import { SANDBOX_STATE_VALUES } from "../api/types.gen";
import { sandboxDot } from "./sandbox";

describe("sandboxDot", () => {
  it("colours each state as the project card does", () => {
    expect(
      Object.fromEntries(
        SANDBOX_STATE_VALUES.map((state) => [state, sandboxDot({ state })]),
      ),
    ).toEqual({
      none: "down",
      // Setup or stopping with nothing running did not finish.
      setup: "failed",
      warm: "warm",
      stopping: "failed",
      stopped: "down",
      failed: "failed",
      deleting: "busy",
    });
  });

  it("shows any state as in progress while an operation runs", () => {
    expect(
      Object.fromEntries(
        SANDBOX_STATE_VALUES.map((state) => [
          state,
          sandboxDot({ state, busy: true }),
        ]),
      ),
    ).toEqual(Object.fromEntries(SANDBOX_STATE_VALUES.map((s) => [s, "busy"])));
  });
});
