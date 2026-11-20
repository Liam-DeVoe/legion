import { expect, test } from "bun:test";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { rowLimit } from "./bounded-subprocess";

const packageDirectory = join(import.meta.dir, "../..");
const visualRun = join(packageDirectory, "e2e/visual-run.sh");

/** A `bun` and a `docker` that carry the shell runner through its host setup, then make the
 *  container command return 0 without running Playwright. Before the runner checks the reporter's
 *  `.last-run.json`, that exit 0 made a visual command that compared no image pass. */
function writeNoRunBinaries(directory: string): void {
  const bun = join(directory, "bun");
  writeFileSync(
    bun,
    `#!/bin/sh
case "$1" in
e2e/harness-up.ts)
  printf '%s\\n' 'export DISPATCH_E2E_REUSE_SERVERS=1'
  exec sleep 600
  ;;
e2e/visual-seed.ts)
  : > "$2"
  ;;
e2e/visual-relay.ts)
  printf '%s\\n' 'visual-relay: ready'
  exec sleep 600
  ;;
*)
  echo "unexpected bun invocation: $*" >&2
  exit 97
  ;;
esac
`
  );
  chmodSync(bun, 0o755);
  const docker = join(directory, "docker");
  writeFileSync(docker, "#!/bin/sh\nexit 0\n");
  chmodSync(docker, 0o755);
}

test(
  "refuses a visual container that exits without Playwright writing its run record",
  async () => {
    const root = mkdtempSync(join(tmpdir(), "visual-run-no-record-"));
    try {
      const bin = join(root, "bin");
      Bun.spawnSync(["mkdir", "-p", bin]);
      writeNoRunBinaries(bin);
      const output = "e2e/test-results-no-record";
      const run = Bun.spawn(["sh", visualRun, `--output=${output}`], {
        cwd: packageDirectory,
        env: {
          ...process.env,
          HOME: root,
          PATH: `${bin}:${process.env.PATH ?? ""}`,
          TMPDIR: root,
        },
        stderr: "pipe",
        stdout: "pipe",
      });
      const status = await run.exited;
      expect(status).toBe(1);
      expect((await new Response(run.stderr).text()).trim()).toBe(
        `e2e/visual-run.sh: Playwright exited 0 without writing ${output}/.last-run.json.`
      );
    } finally {
      rmSync(root, { force: true, recursive: true });
    }
  },
  rowLimit("short")
);
