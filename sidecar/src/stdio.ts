// The stdio protocol loop: Command lines in on stdin, EventDraft lines out on
// stdout, nothing else on stdout ever (diagnostics go to stderr). The first
// command is the sidecar's own sidecar.start with the session specification.

import { appendFileSync } from "node:fs";
import { createInterface } from "node:readline";
import type { Readable, Writable } from "node:stream";

import type { EventDraft, TypedCommand } from "./contract/events.gen.js";
import { decodeCommand, VERSION } from "./events.js";
import { fitDraft } from "./limits.js";
import { parseStartSpec, START_KIND, type StartSpec } from "./options.js";
import { Session, type QueryFn } from "./session.js";
import { notice } from "./translate.js";

// DraftWriter writes one draft per line in call order and waits for the pipe
// to drain when it is full, so a slow reader slows the session down instead
// of growing memory.
export class DraftWriter {
  private tail: Promise<void> = Promise.resolve();
  private readonly out: Writable;
  private broken = false;

  constructor(out: Writable) {
    this.out = out;
    out.on("error", () => {
      this.broken = true;
    });
  }

  // write resolves true once the line is written, false for a draft that
  // cannot be brought under the line limit (a request: the caller answers
  // it instead).
  write(d: EventDraft): Promise<boolean> {
    const fitted = fitDraft(d);
    if (fitted === undefined) return this.tail.then(() => false);
    const line = `${fitted}\n`;
    const next = this.tail.then(
      () =>
        new Promise<void>((resolve) => {
          if (this.broken || this.out.writableEnded) {
            resolve();
            return;
          }
          if (this.out.write(line)) resolve();
          else {
            const done = () => {
              this.out.off("drain", done);
              this.out.off("close", done);
              resolve();
            };
            this.out.on("drain", done);
            this.out.on("close", done);
          }
        }),
    );
    this.tail = next;
    return next.then(() => true);
  }

  // flush resolves when every line written so far has left the process.
  flush(): Promise<void> {
    return this.tail.then(
      () =>
        new Promise<void>((resolve) => {
          if (this.broken || this.out.writableEnded) resolve();
          else this.out.write("", () => resolve());
        }),
    );
  }
}

// Recorder appends every raw SDK message and permission callback to a file,
// one JSON object per line, for turning a real session into fixtures.
export function recorder(path: string, log: (msg: string) => void) {
  let failed = false;
  return (entry: Record<string, unknown>): void => {
    if (failed) return;
    try {
      appendFileSync(
        path,
        `${JSON.stringify({ ts: new Date().toISOString(), ...entry })}\n`,
      );
    } catch (err) {
      failed = true;
      log(
        `recording stopped: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  };
}

export type Line =
  | { type: "start"; id: string; spec: StartSpec }
  | { type: "command"; command: TypedCommand }
  | { type: "invalid"; reason: string; id?: string }
  | { type: "skipped"; reason: string; id: string; kind: string };

function has(
  o: Record<string, unknown>,
  k: string,
  t: "string" | "object",
): boolean {
  return t === "object"
    ? o[k] !== null && typeof o[k] === "object" && !Array.isArray(o[k])
    : typeof o[k] === "string";
}

// payloadError checks the fields the sidecar relies on; the host sends
// contract types, so this guards against a broken line, not a hostile one.
function payloadError(c: TypedCommand): string | undefined {
  const d = c.data as Record<string, unknown> | null;
  if (d === null || typeof d !== "object" || Array.isArray(d)) return "data";
  switch (c.kind) {
    case "message.send":
      if (!has(d, "messageId", "string")) return "messageId";
      if (!has(d, "text", "string")) return "text";
      break;
    case "permission.reply":
    case "question.reply":
    case "plan.reply":
      if (!has(d, "requestId", "string")) return "requestId";
      if (c.kind !== "question.reply" && !has(d, "decision", "string"))
        return "decision";
      if (
        c.kind === "question.reply" &&
        d.answers !== undefined &&
        !Array.isArray(d.answers)
      ) {
        return "answers";
      }
      break;
  }
  return undefined;
}

export function parseLine(line: string): Line {
  let v: unknown;
  try {
    v = JSON.parse(line);
  } catch {
    return { type: "invalid", reason: "not_json" };
  }
  if (v === null || typeof v !== "object" || Array.isArray(v)) {
    return { type: "invalid", reason: "not_object" };
  }
  const o = v as Record<string, unknown>;
  const id = typeof o.id === "string" ? o.id : undefined;
  if (o.kind === START_KIND) {
    if (id === undefined) return { type: "invalid", reason: "id" };
    const spec = parseStartSpec(o.data);
    if (typeof spec === "string")
      return { type: "invalid", reason: `start.${spec}`, id };
    return { type: "start", id, spec };
  }
  if (id === undefined || id === "") return { type: "invalid", reason: "id" };
  if (typeof o.v !== "number" || o.v > VERSION) {
    return { type: "skipped", reason: "version", id, kind: String(o.kind) };
  }
  const c = decodeCommand(line);
  if (c === null)
    return { type: "skipped", reason: "kind", id, kind: String(o.kind) };
  const bad = payloadError(c);
  if (bad !== undefined) return { type: "invalid", reason: `data.${bad}`, id };
  return { type: "command", command: c };
}

export interface SidecarIO {
  stdin: Readable;
  stdout: Writable;
  stderr: Writable;
}

export interface SidecarDeps {
  query: QueryFn;
  env: Record<string, string | undefined>;
  // recordPath turns the recorder on.
  recordPath?: string;
  // signal ends the sidecar as a closed stdin does (SIGTERM).
  signal?: AbortSignal;
  interruptGraceMs?: number;
  now?: () => number;
}

// runSidecar serves one session until stdin closes or a host.shutdown is
// done; it resolves once every draft is written.
export async function runSidecar(
  io: SidecarIO,
  deps: SidecarDeps,
): Promise<void> {
  const writer = new DraftWriter(io.stdout);
  const log = (msg: string) => {
    io.stderr.write(`flow-sidecar: ${msg}\n`);
  };
  const record =
    deps.recordPath !== undefined ? recorder(deps.recordPath, log) : undefined;
  const emit = (d: EventDraft) => writer.write(d);
  const answer = (d: EventDraft, cmd?: string) => {
    if (cmd !== undefined) d.cmd = cmd;
    void emit(d);
  };
  let session: Session | undefined;
  const rl = createInterface({ input: io.stdin, crlfDelay: Infinity });
  const stop = () => rl.close();
  deps.signal?.addEventListener("abort", stop, { once: true });
  if (deps.signal?.aborted) stop();

  for await (const raw of rl) {
    if (raw.trim() === "") continue;
    const line = parseLine(raw);
    switch (line.type) {
      case "invalid":
        log(`bad command line: ${line.reason}`);
        answer(
          notice("sidecar_bad_command", "error", {
            reason: line.reason,
            id: line.id,
          }),
          line.id,
        );
        continue;
      case "skipped":
        answer(
          notice("sidecar_command_skipped", "warning", {
            reason: line.reason,
            kind: line.kind,
          }),
          line.id,
        );
        continue;
      case "start":
        if (session !== undefined) {
          answer(
            notice("sidecar_bad_command", "error", { reason: "started" }),
            line.id,
          );
          continue;
        }
        session = new Session(line.spec, {
          query: deps.query,
          emit,
          log,
          ...(record !== undefined && { record }),
          env: deps.env,
          stderr: (data) => {
            io.stderr.write(data);
          },
          ...(deps.now !== undefined && { now: deps.now }),
          ...(deps.interruptGraceMs !== undefined && {
            interruptGraceMs: deps.interruptGraceMs,
          }),
        });
        continue;
      case "command":
        if (session === undefined) {
          answer(
            notice("sidecar_bad_command", "error", { reason: "not_started" }),
            line.command.id,
          );
          continue;
        }
        if (line.command.kind === "host.shutdown") {
          // whenIdle lets the running turn finish; the reader keeps going so
          // its replies (and a later immediate stop) still arrive.
          void session.shutdown(line.command.data.whenIdle === true).then(stop);
          continue;
        }
        session.handle(line.command);
        continue;
    }
  }
  deps.signal?.removeEventListener("abort", stop);
  // stdin closed (the host is gone) or a shutdown finished.
  await session?.shutdown(false);
  await writer.flush();
}
