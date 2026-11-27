// The right panel: diff with repository picker, preview. Owned by the right
// panel area.
export const panel = {
  label: "Diff",
  diff: "Diff",
  preview: "Preview",
  repository: "Repository",
  noChanges: "No changes yet.",
  close: "Close panel",
  refresh: "Refresh diff",
  resize: "Resize panel",
  loading: "Loading the diff…",
  // The meta line: the worktree's branch against the commit it was cut from.
  versus: "{{branch}} vs {{base}} · read-only",
  noChangesAgainst: "No changes against {{base}} yet.",
  noWorktree:
    "No worktree yet. It is created with the session's first message.",
  filesLabel: "Changed files",
  added: "+{{n}}",
  removed: "−{{n}}",
  // Read out with the counts.
  countsLabel: "{{added}} lines added, {{removed}} removed",
  changeAdded: "new file",
  changeDeleted: "deleted",
  changeRenamed: "renamed",
  renamedFrom: "{{from}} → {{to}}",
  binary: "Binary file, not shown.",
  fileTruncated:
    "This file's diff is cut off past the size limit; its counts are complete.",
  diffTruncated:
    "More files changed than shown: the rest is past the file-count or size limit.",
  sandboxDown: "The sandbox is not running, so the diff cannot be read.",
  sandboxStarting: "Starting the sandbox…",
  startSandbox: "Start sandbox",
  failed: "The diff could not be read.",
  retry: "Try again",
  oldLine: "Old line",
  newLine: "New line",
};
