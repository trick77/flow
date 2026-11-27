import { useEffect, useState } from "react";

import { errorCode, getSessionDiff } from "../api";
import type { Diff, ErrorCode } from "../api/types.gen";

export interface DiffState {
  // The latest diff read for this repository (kept while a refresh runs).
  diff?: Diff;
  error?: ErrorCode;
  loading: boolean;
}

// useDiff reads one worktree's diff, again whenever rev changes. An answer
// for another repository or an older rev is dropped. Without a repoId it
// reads nothing.
export function useDiff(
  sessionId: string,
  repoId: string | undefined,
  rev: number,
): DiffState {
  const [state, setState] = useState<DiffState & { key?: string }>({
    loading: repoId !== undefined,
  });
  const key = repoId === undefined ? undefined : `${repoId}\n${rev}`;
  const [asked, setAsked] = useState(key);
  // A new repository or rev: loading at once, the previous diff kept only
  // for the same repository.
  if (asked !== key) {
    setAsked(key);
    const same = state.key?.split("\n")[0] === repoId;
    setState({
      key: state.key,
      diff: same ? state.diff : undefined,
      loading: repoId !== undefined,
    });
  }

  useEffect(() => {
    if (repoId === undefined) return;
    let live = true;
    getSessionDiff(sessionId, repoId).then(
      (diff) => {
        if (live) setState({ key: `${repoId}\n${rev}`, diff, loading: false });
      },
      (err: unknown) => {
        if (live) {
          setState({
            key: `${repoId}\n${rev}`,
            error: errorCode(err),
            loading: false,
          });
        }
      },
    );
    return () => {
      live = false;
    };
  }, [sessionId, repoId, rev]);

  return { diff: state.diff, error: state.error, loading: state.loading };
}
