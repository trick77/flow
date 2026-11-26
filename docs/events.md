# Event model

Flow's provider-neutral contract between agent adapters, flowd, Flow and the UI. The UI knows nothing else.

## Source and bindings

- **Source:** `contract/events.schema.json`, a JSON Schema subset plus `x-unions` (kind → payload tables for `Event`
  and `Command`). The browser API's types come the same way from `backend/api/openapi.json`.
- **Bindings:** `backend/internal/contractgen` generates `backend/internal/event/event.gen.go`,
  `backend/internal/apitypes/apitypes.gen.go`, `sidecar/src/contract/events.gen.ts`, `ui/src/contract/events.gen.ts`
  and `ui/src/api/types.gen.ts`. `TestGenerated` fails on a stale file; regenerate in `backend/` with
  `CONTRACTGEN_UPDATE=1 go test ./internal/contractgen/`. The generator refuses keywords outside the subset.
- Why: one language-neutral source, no dependency, drift caught by the gate. Rejected: hand-kept types in three
  places guarded only by fixtures (TypeScript types vanish at runtime, so fixtures cannot hold them), an IDL
  dependency (codegen toolchain for ~40 types).
- **Fixtures:** `contract/fixtures/events.jsonl` (at least one per event kind; examples, not a stream: sessions,
  times and seqs do not form one log), `demo.jsonl` (one coherent turn, a real stream for the skeleton and the UI
  demo), `commands.jsonl` (one per command kind), `mapping/claude.jsonl` and `mapping/opencode.jsonl` (provider input → expected drafts). The Go suite
  validates every fixture against the schema and round-trips it through the generated types; the sidecar and UI
  suites read the same files. The mapping fixtures are synthetic, written from the documentation: the adapters'
  contract tests replace them with recorded real streams.

## Envelope, ids, ordering

- `v` major version (1); `seq` durable position from 1, gapless per session; `ts` UTC from the host; `session` Flow
  session UUID; `turn` on every event of a turn; `parentTool` = the subagent's tool call; `cmd` = the command that
  caused it.
- Ids: sessions UUIDv7 chosen by Flow (also the Claude `sessionId`); messages = the browser's `clientId`; tools and
  requests keep the provider's ids (`toolu_…`, `requestId`; OpenCode `call_…`, `per_…`, `frm_…`); blocks
  `<message id>:<index>`.
- Order: one writer per log, so durable events are totally ordered by seq. Ephemeral events interleave in emission
  order on the live stream only. Across sessions there is no order.
- Durable vs ephemeral is per kind (`ephemeral` in `x-unions`): deltas, tool progress, live snapshot.

## Versioning and forward compatibility

- Readers skip events with `v` above theirs and kinds they do not know; ignore unknown fields.
- Writers add kinds and optional fields only; renaming, retyping or making a field required is a new major version.
- Enum values are part of the contract: a new value is a minor change readers must tolerate (show a neutral row).

## Status

The host derives `session.status` and writes it on change; Flow mirrors it into `sessions.status`:

| Status | When |
|---|---|
| idle | no turn yet |
| working | a turn runs |
| queued | the turn waits for the check queue |
| approval / question / plan | a permission request / question / plan waits for the owner |
| done / failed | the last turn ended completed or interrupted / error or lost, or its last `gate.finished` had a non-zero exit |

Sidebar sections (UI derivation, mock): Needs you = approval, question, plan; Working = working, queued; Done =
done, failed while `unseen`; Idle = the rest. A group of a parent and its children sorts under its most urgent
member. The spec's "waiting" is `question`/`plan`; "needs approval" is `approval`.

## Mock elements

| Mock element | Expressed by |
|---|---|
| user message, "from parent session" | `message.user` (`origin`, `fromSession`) |
| activity fold, "Worked for 2m 14s" | `turn.started`/`turn.ended` (`durationMs`) and the tool rows between |
| tool rows, "6 results", "+18 −4", "2 of 20 failed", "Opened draft pull request" | `tool.started` (`kind`, `path`, `command`, `pattern`) + `tool.finished.result` |
| assistant text, code blocks, tables | `assistant.text` (Markdown), live `assistant.text_delta` |
| changed-files card | `files.changed` per repo at turn end |
| permission card and its resolution, "capped at Accept edits by the parent" | `permission.requested` (`mode`, `modeCap`), `permission.resolved` |
| question with options, "Answer in the composer" | `question.asked`, `question.resolved` (`answers` or `text`) |
| plan card | `plan.proposed`, `plan.resolved` |
| todo list | `todo.updated` (whole list) |
| subagent row | `tool.started` kind `subagent` + events with `parentTool` |
| session-resumed divider | `session.resumed` |
| working indicator, "running make check" | status `working` + the open `tool.started` |
| queued message, Send now, Remove | `message.queued`/`message.dequeued`; commands `message.send_now`, `message.remove` |
| check-queue wait, "position 2, after riptide's" | `gate.queued` (`position`, `aheadRepo`), status `queued` |
| turn ended with failing check | `gate.finished` (`exitCode`) + `turn.ended` |
| repositories card | `worktree.added` + API `Session.worktrees` (per-repo state, +/−) |
| pull-request set | `pull_requests.updated` + API `listPullRequests` |
| sessions-started block, "Reported back from loom" | `child.started`, `child.reported`; child side `session.reported` |
| per-repo diffs | API `getSessionDiff` |
| statuses and sidebar sections | `session.status`, API `SessionSummary.status`/`unseen` |
| sandbox chip, setup log, Claude signed in, config sync line | API `Sandbox.state`, `getSetupLog`, `Connections.claude`, `Profile.configSync` |

**Provisional:** the PR set's free-text `note` ("Opens once v0.9.0 is tagged") has no source yet; it needs an agent
tool the spec does not list.

## Claude Agent SDK → Flow

Sources: [typescript](https://code.claude.com/docs/en/agent-sdk/typescript),
[user-input](https://code.claude.com/docs/en/agent-sdk/user-input),
[streaming-output](https://code.claude.com/docs/en/agent-sdk/streaming-output),
[subagents](https://code.claude.com/docs/en/agent-sdk/subagents),
[todo-tracking](https://code.claude.com/docs/en/agent-sdk/todo-tracking).

| SDK | Flow |
|---|---|
| `system`/`init` (`session_id`, `model`, `permissionMode`, `cwd`) | `session.started` (first start) |
| `stream_event` `content_block_delta` `text_delta` | `assistant.text_delta` (`blockId` = message id : block index) |
| `assistant` content `text` | `assistant.text` |
| `assistant` content `thinking` | `assistant.thinking` |
| `assistant` content `tool_use` | `tool.started` (`kind` from the tool name: Read→read, Edit/MultiEdit→edit, Write→write, Grep/Glob→search, Bash→bash, WebFetch/WebSearch→web, TodoWrite/Task*→todo, Agent/Task→subagent, AskUserQuestion→question, ExitPlanMode→plan, `mcp__*`→mcp) |
| `user` content `tool_result` (`is_error`) | `tool.finished` (`ok`/`error`; `result` from the tool's structured output where present) |
| any message with `parent_tool_use_id` | same mapping, `parentTool` set |
| `tool_progress` | `tool.progress` |
| `TodoWrite` input `todos[{content,status,activeForm}]`; `TaskCreate`/`TaskUpdate` | `todo.updated` (the sidecar keeps the list) |
| `canUseTool(toolName, input, {toolUseID, requestId, suggestions})` | `permission.requested` (`canAlwaysAllow` = suggestions present) |
| `canUseTool("AskUserQuestion", {questions})` | `question.asked`; reply = allow with `updatedInput.answers` keyed by question text |
| `canUseTool("ExitPlanMode", {plan?})` | `plan.proposed` (**provisional**: approval path not documented; allow = approve, deny + message = keep planning) |
| `permission.reply` allow_once / allow_always / deny | `{behavior:"allow"}` / allow + `updatedPermissions` = suggestions / `{behavior:"deny", message}` |
| `result` `success` | `turn.ended {completed}` with `usage`, `total_cost_usd`, `duration_ms` |
| `result` `error_during_execution` after `interrupt()` | `turn.ended {interrupted}` |
| `result` other `error_*` | `turn.ended {error, errorCode}` (max_turns, budget, …) |
| `system`/`compact_boundary` | `notice {context_compacted}` |
| `rate_limit_event`, `system`/`api_retry`, `auth_status` errors | `notice` (`rate_limited`, `api_retry`, `auth_required`) |

## OpenCode (V2 API) → Flow

Source: [packages/protocol/openapi.json](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/openapi.json)
at dev `8ec08f49`, docs [opencode.ai/docs/api](https://opencode.ai/docs/api). Events from `GET /api/event`, filtered
to the session's `sessionID`; envelope `{id, type, location, data}`.

| OpenCode | Flow |
|---|---|
| `session.text.delta` (`assistantMessageID`, `ordinal`, `delta`) | `assistant.text_delta` |
| `session.text.ended` (`text`) | `assistant.text` |
| `session.reasoning.ended` | `assistant.thinking` |
| `session.tool.called` (`id`, `name`, `input`) | `tool.started` |
| `session.tool.progress` | `tool.progress` |
| `session.tool.success` / `session.tool.failed` | `tool.finished` ok / error |
| `permission.asked` (`id`, `action`, `resources`, `source.id`) | `permission.requested`; reply `POST /api/session/{id}/permission/{requestID}/reply {decision: once\|always\|reject}` |
| `form.created` with `metadata.kind = question` | `question.asked`; reply `POST /api/session/{id}/form/{formID}/reply {answer}` |
| `todowrite` tool calls | `todo.updated` (no todo event in V2) |
| `session.execution.started` | (host already wrote `turn.started`) |
| `session.execution.succeeded` / `.interrupted` / `.failed` | `turn.ended` completed / interrupted / error |
| `session.step.ended` (`cost`, `tokens`) | summed into `turn.ended.usage`, `costUsd` |
| `session.retry.scheduled`, `session.compaction.ended` | `notice` |
| reconnect | `GET /api/session/{id}/message` paged by cursor; drafts for parts after the last logged ones |

Plan mode: agent `plan`; OpenCode has no plan-approval request, so `plan.proposed` is not produced for OpenCode
sessions (the plan arrives as text).

## Commands

| Command | From | Effect |
|---|---|---|
| `message.send` | Flow (browser message, parent session) | idle: `message.user`, `turn.started`, deliver; busy: `message.queued` (or interrupt) |
| `message.send_now`, `message.remove` | Flow | interrupt + deliver a queued one / drop it |
| `turn.interrupt` | Flow | `interrupt()` / OpenCode interrupt |
| `permission.reply`, `question.reply`, `plan.reply` | Flow (owner only) | resolve the open request |
| `settings.set` | Flow | `setModel`/`setPermissionMode`, `session.settings` |
| `gate.update` | Flow's gate queue | `gate.*`, status `queued`/`working` |
| `child.start`, `child.report`, `report.sent` | Flow | `child.started`, queued `child.reported` + message, `session.reported` |
| `worktree.add`, `pull_requests.set` | Flow | `worktree.added`, `pull_requests.updated` |
| `host.shutdown` | flowd | stop (now or when idle) for upgrade, stop, archive |
