import type { SessionStatus } from "../api/types.gen";
import { statusClass, statusKey } from "./status";

describe("status", () => {
  it("names each status and picks its dot", () => {
    expect(statusKey("approval")).toBe("statusApproval");
    expect(statusKey("queued")).toBe("statusQueued");
    expect(statusClass("plan")).toBe("st-question");
    expect(statusClass("queued")).toBe("st-working");
    expect(statusClass("failed")).toBe("st-failed");
  });

  it("reads an unknown status as idle", () => {
    const newer = "paused" as SessionStatus;
    expect(statusKey(newer)).toBe("statusIdle");
    expect(statusClass(newer)).toBe("st-idle");
  });
});
