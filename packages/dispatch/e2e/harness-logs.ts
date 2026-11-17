/** Where each stack's processes write their output, and how a row reads what its own Dispatch
 *  server logged there.
 *
 *  `e2e/harness-pool.ts` gives every stack a log directory under the run's own, a run of one
 *  stack included, and `e2e/harness-start.ts` writes each process's output to `<id>.log` in it.
 *  The directory rides in the stack's record of the manifest under `stackLogDirVariable`, so a
 *  worker finds its own stack's through `stackValue` (e2e/harness-ports.ts), as it finds its
 *  ports. A run the pool started no stack for - a deployed server, or a harness a person left
 *  listening (`DISPATCH_E2E_REUSE_SERVERS`) - carries none, and a row that reads the log skips
 *  there rather than reading nothing and passing.
 *
 *  The access log is Dispatch's one line per served request
 *  (`packages/envoy/internal/dispatch/accesslog`): slog's text form of `msg="dispatch: request"`
 *  with the method, the route pattern, the path, the caller and the status, and never the query.
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { stackValue } from "./harness-ports";

/** The variable a stack's record carries its log directory under. */
export const stackLogDirVariable = "DISPATCH_E2E_LOG_DIR";

/** The log directory of stack `index` under the run's directory `runDir`. */
export function stackLogDir(runDir: string, index: number): string {
  return join(runDir, `worker-${index}`, "log");
}

/** The ids the Dispatch server's log files are named by: the one server, or A and B in
 *  two-process mode (`dispatchListeners`, e2e/harness-listeners.ts). The round-robin proxy in
 *  front of A and B logs no request line of its own. */
const dispatchLogIds = ["dispatch", "dispatch-a", "dispatch-b"] as const;

/** One request line of the access log, as far as a row reads it. */
export interface AccessLine {
  readonly method: string;
  /** The request's path, ids and all, without its query, which the line never carries. */
  readonly path: string;
  readonly status: number;
}

/** The access log's message, matched whole: a warning about the previous shared token starts
 *  with the same words (`accesslog.Message`). */
const accessMessage = ' msg="dispatch: request" ';

/** One attribute's value on a slog text line: bare, or quoted as Go's `strconv.Quote` writes it,
 *  which JSON reads. */
function attribute(line: string, name: string): string | undefined {
  const found = new RegExp(` ${name}=("(?:[^"\\\\]|\\\\.)*"|\\S+)`).exec(line)?.[1];
  if (found === undefined) return undefined;
  return found.startsWith('"') ? (JSON.parse(found) as string) : found;
}

/** The Dispatch server's log files of this process's stack, or undefined where the run gave the
 *  stack no log directory. */
function dispatchLogs(env: NodeJS.ProcessEnv): string[] | undefined {
  const dir = stackValue(env, stackLogDirVariable);
  if (dir === undefined || dir === "") return undefined;
  return dispatchLogIds.map((id) => join(dir, `${id}.log`)).filter((file) => existsSync(file));
}

/** Whether this process's stack writes the Dispatch server's access log to a file a row can read:
 *  a run whose stacks the pool started, and no other. */
export function accessLogReadable(env: NodeJS.ProcessEnv = process.env): boolean {
  return (dispatchLogs(env)?.length ?? 0) > 0;
}

/** Every request line the Dispatch server of this process's stack has logged so far, in the order
 *  each file holds them (both servers' in two-process mode, A's first). */
export function accessLines(env: NodeJS.ProcessEnv = process.env): AccessLine[] {
  const files = dispatchLogs(env);
  if (files === undefined || files.length === 0) {
    throw new Error(
      `this run gave the stack no log directory (${stackLogDirVariable}), so the Dispatch ` +
        "server's access log is in no file: only a run whose stacks e2e/harness-pool.ts started has one"
    );
  }
  const lines: AccessLine[] = [];
  for (const file of files) {
    for (const line of readFileSync(file, "utf8").split("\n")) {
      if (!line.includes(accessMessage)) continue;
      const method = attribute(line, "method");
      const path = attribute(line, "path");
      const status = Number(attribute(line, "status"));
      if (method === undefined || path === undefined || !Number.isInteger(status)) continue;
      lines.push({ method, path, status });
    }
  }
  return lines;
}
