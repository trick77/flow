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
      setup: "busy",
      warm: "warm",
      stopping: "busy",
      stopped: "down",
      failed: "failed",
      deleting: "busy",
    });
  });
});
