import { copyText } from "./clipboard";

describe("copyText", () => {
  const exec = vi.fn<(cmd: string) => boolean>();

  beforeEach(() => {
    exec.mockReset();
    Object.defineProperty(document, "execCommand", {
      value: exec,
      configurable: true,
      writable: true,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Reflect.deleteProperty(navigator, "clipboard");
  });

  function clipboard(writeText: (t: string) => Promise<void>) {
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
  }

  it("uses the Clipboard API where it exists", async () => {
    const write = vi.fn(async () => undefined);
    clipboard(write);
    await expect(copyText("ABCD-1234")).resolves.toBe(true);
    expect(write).toHaveBeenCalledWith("ABCD-1234");
    expect(exec).not.toHaveBeenCalled();
  });

  it("falls back to execCommand when the API refuses", async () => {
    clipboard(async () => {
      throw new Error("denied");
    });
    exec.mockReturnValue(true);
    await expect(copyText("ABCD-1234")).resolves.toBe(true);
    expect(exec).toHaveBeenCalledWith("copy");
    // The helper field is gone again.
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls back without the API (plain HTTP)", async () => {
    exec.mockReturnValue(false);
    await expect(copyText("x")).resolves.toBe(false);
    exec.mockImplementation(() => {
      throw new Error("no");
    });
    await expect(copyText("x")).resolves.toBe(false);
  });

  it("gives up when neither exists", async () => {
    Object.defineProperty(document, "execCommand", {
      value: undefined,
      configurable: true,
      writable: true,
    });
    await expect(copyText("x")).resolves.toBe(false);
  });
});
