import { act, renderHook } from "@testing-library/react";

import { pageActive, useMarkSeen } from "./useMarkSeen";

type Props = Parameters<typeof useMarkSeen>[0];

describe("useMarkSeen", () => {
  const fetchMock = vi.fn<typeof fetch>();
  let focused = true;

  beforeEach(() => {
    vi.useFakeTimers();
    fetchMock.mockReset();
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    focused = true;
    vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const seqs = () =>
    fetchMock.mock.calls.map((c) => JSON.parse(String(c[1]?.body)).seq);

  function hook(over: Partial<Props> = {}) {
    const props: Props = {
      sessionId: "s1",
      lastSeq: 3,
      unseen: true,
      shown: true,
      onSeen: vi.fn(),
      ...over,
    };
    const r = renderHook((p: Props) => useMarkSeen(p), { initialProps: props });
    return { ...r, props };
  }

  it("marks once per interval, with the newest seq", async () => {
    const { rerender, props } = hook();
    rerender({ ...props, lastSeq: 4 });
    rerender({ ...props, lastSeq: 5 });
    expect(fetchMock).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTime(1000));
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/s1/seen");
    expect(seqs()).toEqual([5]);
    expect(props.onSeen).toHaveBeenCalledTimes(1);
    // Nothing new: nothing sent.
    rerender({ ...props, lastSeq: 5 });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(seqs()).toEqual([5]);
    rerender({ ...props, lastSeq: 6 });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(seqs()).toEqual([5, 6]);
  });

  it("marks the same seq again when the session turns unseen without a new event", async () => {
    const { rerender, props } = hook({ lastSeq: 5 });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(seqs()).toEqual([5]);
    // Seen; then a sandbox stop cuts the turn off: unseen, no new seq.
    rerender({ ...props, lastSeq: 5, unseen: false });
    rerender({ ...props, lastSeq: 5, unseen: true });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(seqs()).toEqual([5, 5]);
  });

  it("waits while the session is seen, hidden, or the tab lacks focus", async () => {
    const { rerender, props } = hook({ unseen: false });
    await act(async () => vi.advanceTimersByTime(1000));
    rerender({ ...props, unseen: true, shown: false });
    await act(async () => vi.advanceTimersByTime(1000));
    rerender({ ...props, unseen: true, lastSeq: 0 });
    await act(async () => vi.advanceTimersByTime(1000));
    focused = false;
    act(() => {
      window.dispatchEvent(new Event("blur"));
    });
    rerender({ ...props, unseen: true });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(fetchMock).not.toHaveBeenCalled();
    focused = true;
    act(() => {
      window.dispatchEvent(new Event("focus"));
    });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(seqs()).toEqual([3]);
  });

  it("drops a pending mark when the session is left", async () => {
    const { rerender, props } = hook();
    await act(async () => vi.advanceTimersByTime(500));
    rerender({ ...props, shown: false });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("after a failure waits for the seq to advance", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "internal" }), { status: 500 }),
    );
    const { rerender, props } = hook();
    await act(async () => vi.advanceTimersByTime(1000));
    rerender({ ...props, lastSeq: 3 });
    await act(async () => vi.advanceTimersByTime(2000));
    expect(seqs()).toEqual([3]);
    expect(props.onSeen).not.toHaveBeenCalled();
    rerender({ ...props, lastSeq: 4 });
    await act(async () => vi.advanceTimersByTime(1000));
    expect(seqs()).toEqual([3, 4]);
  });

  it("stops for good when the server has no seen marks yet", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "not_implemented" }), {
        status: 501,
      }),
    );
    const { rerender, props } = hook();
    await act(async () => vi.advanceTimersByTime(1000));
    rerender({ ...props, lastSeq: 9 });
    await act(async () => vi.advanceTimersByTime(2000));
    expect(seqs()).toEqual([3]);
  });

  it("reads the page's visibility and focus", () => {
    expect(pageActive()).toBe(true);
    focused = false;
    expect(pageActive()).toBe(false);
  });
});
