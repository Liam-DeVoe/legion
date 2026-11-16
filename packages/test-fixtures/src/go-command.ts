/**
 * A go command a test starts, run so that what go writes on its standard error reaches this
 * process's as go writes it. A test that only collects go's standard error shows nothing until go
 * exits, and a module download that stalls into the test's timeout never exits: the timeout ends
 * the test with the bytes unread, and the log does not name the download.
 */
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/** Where a go command runs: its directory, and its whole environment (this process's when unset). */
export interface GoCommand {
  readonly cwd: string;
  readonly env?: Record<string, string | undefined>;
}

/**
 * Runs `go <args>` and returns its standard output, with go's standard error going to this
 * process's as go writes it. It throws when go exits non-zero, naming the command and carrying
 * what go wrote on both streams.
 */
export async function goOutput(args: readonly string[], command: GoCommand): Promise<string> {
  const child = Bun.spawn(["go", ...args], {
    cwd: command.cwd,
    env: command.env ?? process.env,
    stdout: "pipe",
    stderr: "pipe",
  });
  const errors: Uint8Array[] = [];
  const copied = (async () => {
    for await (const chunk of child.stderr) {
      process.stderr.write(chunk);
      errors.push(chunk);
    }
  })();
  const [stdout, code] = await Promise.all([
    new Response(child.stdout).text(),
    child.exited,
    copied,
  ]);
  if (code !== 0) throw failure(args, command, code, stdout, Buffer.concat(errors).toString());
  return stdout;
}

/**
 * {@link goOutput} for a caller that cannot wait on a promise, with `stdin` as go's standard
 * input (this process's when unset). A synchronous spawn hands back a pipe's bytes only once go
 * exits, so bash tees go's standard error: to this process's as go writes it, and to a file the
 * failure reads.
 */
export function goOutputSync(
  args: readonly string[],
  command: GoCommand & { readonly stdin?: Uint8Array }
): string {
  const scratch = mkdtempSync(join(tmpdir(), "go-output-"));
  try {
    const kept = join(scratch, "stderr");
    const run = Bun.spawnSync(["bash", "-c", TEE, kept, ...args], {
      cwd: command.cwd,
      env: command.env ?? process.env,
      stdin: command.stdin ?? "inherit",
      stdout: "pipe",
      stderr: "inherit",
    });
    const stdout = run.stdout.toString();
    if (run.exitCode !== 0) {
      throw failure(args, command, run.exitCode, stdout, readFileSync(kept, "utf8"));
    }
    return stdout;
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

/**
 * go with its standard error piped through tee, which writes it to bash's own standard error and
 * to the file named by $0, while go's standard output keeps bash's (fd 3 carries it past the
 * pipe). A pipeline is waited for whole, so the file is complete when bash exits with go's status.
 */
const TEE =
  '{ go "$@" 2>&1 1>&3 3>&- | tee -- "$0" >&2 3>&-; status=$PIPESTATUS; } 3>&1; exit "$status"';

function failure(
  args: readonly string[],
  command: GoCommand,
  code: number | null,
  stdout: string,
  stderr: string
) {
  return new Error(`go ${args.join(" ")} in ${command.cwd} exited ${code}:\n${stdout}${stderr}`);
}
