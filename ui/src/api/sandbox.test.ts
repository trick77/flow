import { sandboxBusy, sandboxStale, sandboxStartable } from "./sandbox";

describe("sandbox state for the controls", () => {
  it("reads busy only when the server says so", () => {
    expect(sandboxBusy(undefined)).toBe(false);
    expect(sandboxBusy({})).toBe(false);
    expect(sandboxBusy({ busy: true })).toBe(true);
  });

  it("calls setup and stopping without busy left behind", () => {
    expect(sandboxStale({ state: "setup" })).toBe(true);
    expect(sandboxStale({ state: "stopping" })).toBe(true);
    expect(sandboxStale({ state: "setup", busy: true })).toBe(false);
    expect(sandboxStale({ state: "failed" })).toBe(false);
    expect(sandboxStale(undefined)).toBe(false);
  });

  it("offers a start only when nothing runs and the sandbox is down", () => {
    expect(sandboxStartable(undefined)).toBe(true);
    for (const state of [
      "none",
      "stopped",
      "failed",
      "setup",
      "stopping",
    ] as const) {
      expect(sandboxStartable({ state })).toBe(true);
      expect(sandboxStartable({ state, busy: true })).toBe(false);
    }
    expect(sandboxStartable({ state: "warm" })).toBe(false);
    expect(sandboxStartable({ state: "deleting" })).toBe(false);
  });
});
