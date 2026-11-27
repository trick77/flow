// Entry point: node dist/main.js [--record <file>]. The session host starts
// it as its child, writes sidecar.start and then commands to stdin, and reads
// event drafts from stdout.

import { parseArgs } from "node:util";

import { query } from "@anthropic-ai/claude-agent-sdk";

import { runSidecar } from "./stdio.js";

// stdout carries the protocol only: anything a library logs goes to stderr.
console.log = console.error;
console.info = console.error;
console.debug = console.error;

const { values } = parseArgs({
  options: { record: { type: "string" } },
  strict: true,
});

const stop = new AbortController();
for (const sig of ["SIGTERM", "SIGINT"] as const) {
  process.once(sig, () => stop.abort());
}
process.stdout.on("error", (err) => {
  // The host closed its end: nobody reads the drafts any more.
  process.stderr.write(`flow-sidecar: stdout: ${err.message}\n`);
  process.exit(1);
});

await runSidecar(
  { stdin: process.stdin, stdout: process.stdout, stderr: process.stderr },
  {
    query,
    env: process.env,
    signal: stop.signal,
    ...(values.record !== undefined && { recordPath: values.record }),
  },
);
// Claude Code is closed and every line is out; nothing else must keep the
// process alive.
process.exit(0);
