import { beforeAll, describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// lib/leftovers.sh: the one mark and the one removal of a shell stack's Postgres
// (packages/dispatch/e2e/postgres-mark.ts), and refuse_leftovers, which names what else an earlier
// run of a stage left. The image rows of packages/dispatch/e2e/__tests__/harness-postgres-leftover
// .test.ts start real shell stacks' containers and remove them; these rows hold each driver to the
// two calls and refuse_leftovers to its reading of container names, with a `docker` of their own.

const lib = join(import.meta.dir, "leftovers.sh");
const e2e = join(import.meta.dir, "..");
const root = join(e2e, "..", "..");
const dir = temporaryDirectory("leftovers-test");

/** Each shell stack's start that starts a Postgres container, by its path under the repository:
 *  every driver of scripts/e2e that names a Postgres image, lib/session-db.sh, which starts the
 *  session database of Stage 4b and of the sandbox Claude Code proof, and the skill scenarios'
 *  batch. */
const drivers = [
  "scripts/e2e/claude-backend-sandbox.sh",
  "scripts/e2e/claude-backend-tmux.sh",
  "scripts/e2e/controller-start-tmux.sh",
  "scripts/e2e/controller-sweep-kind.sh",
  "scripts/e2e/dispatch-user-turns.sh",
  "scripts/e2e/lib/session-db.sh",
  "scripts/e2e/parked-decision-devbox.sh",
  "scripts/e2e/stage1-skeleton.sh",
  "scripts/e2e/stage2-tmux-supervision.sh",
  "scripts/e2e/stage3-devbox-workflow.sh",
  "scripts/e2e/stage4a-sandbox-runtime.sh",
  "scripts/e2e/stage4b-sandbox-tree.sh",
  "scripts/e2e/verifiers-staging-token.sh",
  "packages/pi-legion/scripts/skill-scenarios/rig.sh",
];

/** A Postgres image as the drivers name one: the pinned pgvector or PostgreSQL digest, through
 *  the mirror or through the repository's own copy, or the session database's image variable. */
const postgresImage =
  /pgvector\/pgvector@sha256:|library\/postgres@sha256:|legion\/ci@sha256:(?:2358fcba|979bccfd)|"\$session_db_image"/;

/** The two calls as a driver writes them: the removal, and the mark of one container, into a
 *  variable the next line's create passes as its `--label`. */
const sweepLine = /^\s*remove_leftover_postgres \|\| fail .*$/gm;
const markLine = /^(\s*)(\w+)=\$\(postgres_mark "\$work" ("[^"]+")\) \|\| fail .*$/gm;

describe("each shell stack's start that starts a Postgres runs the one removal and marks its container", () => {
  test("every driver that names a Postgres image is one of the list", () => {
    const named = [
      ...new Bun.Glob("scripts/e2e/*.sh").scanSync({ cwd: root }),
      ...new Bun.Glob("scripts/e2e/lib/*.sh").scanSync({ cwd: root }),
      "packages/pi-legion/scripts/skill-scenarios/rig.sh",
    ].filter((path) => postgresImage.test(readFileSync(join(root, path), "utf8")));
    expect(named.sort()).toEqual([...drivers].sort());
  });

  for (const driver of drivers) {
    test(`${driver}: its two lines run as it calls them, and its create carries the mark`, () => {
      const text = readFileSync(join(root, driver), "utf8");
      const sweeps = [...text.matchAll(sweepLine)].map((match) => match[0].trim());
      const marks = [...text.matchAll(markLine)];
      expect({ sweeps: sweeps.length, marks: marks.length }).toEqual({
        sweeps: marks.length,
        marks: expect.any(Number),
      });
      expect(marks.length).toBeGreaterThan(0);
      for (const [line, , variable = "", container = ""] of marks) {
        // The command after the mark creates the container with it as a label.
        const after = text.slice(text.indexOf(line) + line.length + 1).split("\n")[0] ?? "";
        expect({ after, label: after.includes(`--label "$${variable}"`) }).toEqual({
          after,
          label: true,
        });
        // Every variable the container's name is built from, set to a value of this row's.
        const names = [...container.matchAll(/\$\{?(\w+)/g)].map((match) => match[1]);
        const set = names.map((name) => `${name}=row-${name}`).join("\n");
        const script = `set -euo pipefail
. ${JSON.stringify(lib)}
fail() { echo "FAIL: $*" >&2; exit 1; }
work=$(mktemp -d ${JSON.stringify(join(dir, "work.XXXXXX"))})
${set}
${sweeps[0]}
${line.trim()}
printf '%s\\n%s\\n%s\\n' "$$" "$work" "$${variable}"
printf '%s\\n' ${container}`;
        const run = Bun.spawnSync(["bash", "-c", script], { env: process.env });
        const stderr = run.stderr.toString();
        expect({ code: run.exitCode, stderr }).toEqual({ code: 0, stderr: expect.any(String) });
        expect(stderr).not.toContain("FAIL:");
        const [pid = "", work = "", label = "", name = ""] = run.stdout.toString().split("\n");
        const prefix = "dev.legion.dispatch-e2e.postgres-mark=";
        expect(label.startsWith(prefix)).toBe(true);
        const mark = JSON.parse(label.slice(prefix.length)) as {
          owner: { pid: number };
          run: string;
          container: string;
          sockets?: string;
        };
        expect({
          owner: mark.owner.pid,
          run: mark.run,
          container: mark.container,
          sockets: mark.sockets,
        }).toEqual({ owner: Number(pid), run: work, container: name, sockets: undefined });
      }
    });
  }

  test("a start with no bun on PATH fails with a line that names it", () => {
    const run = Bun.spawnSync(
      [
        "/bin/bash",
        "-c",
        `. ${JSON.stringify(lib)}; remove_leftover_postgres || echo "refused $?"`,
      ],
      { env: { PATH: "/nonexistent" } }
    );
    expect(run.stdout.toString()).toBe("refused 1\n");
    expect(run.stderr.toString()).toContain("lib/leftovers.sh: bun is required");
  });
});

/** A `docker` that answers refuse_leftovers from a fixture: `ps` prints each line of
 *  $FAKE_PS_LINES, and `inspect -f {{.Created}}` the time of $FAKE_CREATED. */
const fakeDocker = `#!/usr/bin/env bash
case "$1" in
  ps) printf '%s\\n' "$FAKE_PS_LINES" ;;
  inspect) printf '%s\\n' "$FAKE_CREATED" ;;
  *) exit 64 ;;
esac
`;

/** A pid no process of this machine has: one this row started and waited for. */
function deadPid(): number {
  const child = Bun.spawnSync(["bash", "-c", "echo $$"]);
  return Number(child.stdout.toString().trim());
}

describe("refuse_leftovers", () => {
  const bin = join(dir, "bin");
  beforeAll(() => {
    mkdirSync(bin);
    writeFileSync(join(bin, "docker"), fakeDocker);
    chmodSync(join(bin, "docker"), 0o755);
  });

  // refuse_leftovers PREFIX run with the fake docker; the prefix is this row's, so no process of
  // the machine carries its work directory.
  function refuse(prefix: string, lines: string[]) {
    const run = Bun.spawnSync(
      [
        "bash",
        "-c",
        `. ${JSON.stringify(lib)}; fail() { echo "FAIL: $*"; exit 1; }; refuse_leftovers ${prefix}; echo passed`,
      ],
      {
        env: {
          ...process.env,
          PATH: `${bin}:${process.env.PATH ?? ""}`,
          FAKE_PS_LINES: lines.join("\n"),
          FAKE_CREATED: new Date().toISOString(),
        },
      }
    );
    return run.stdout.toString();
  }

  test("a dead run's NATS container is refused under main's name and under the pid and random name; a marked Postgres is the removal's, and a live run's container is no leftover", () => {
    const prefix = `legion-e2erow${process.pid}`;
    const dead = deadPid();
    const live = process.pid;
    const out = refuse(prefix, [
      `${prefix}-nats-${dead}|`,
      `${prefix}-nats-${dead}-0123456789abcdef|`,
      `${prefix}-pg-${dead}-0123456789abcdef|{"owner":{"pid":${dead}}}`,
      `${prefix}-nats-${live}-fedcba9876543210|`,
    ]);
    expect(out).toStartWith(
      `FAIL: an earlier ${prefix} run whose driver is gone left these running:`
    );
    expect(out).toContain(
      `container ${prefix}-nats-${dead} (docker rm -f -v ${prefix}-nats-${dead})`
    );
    expect(out).toContain(`container ${prefix}-nats-${dead}-0123456789abcdef (`);
    expect(out).not.toContain(`${prefix}-pg-`);
    expect(out).not.toContain(`${prefix}-nats-${live}-fedcba9876543210`);
  });

  test("a run whose every container is a live run's, or marked, passes", () => {
    const prefix = `legion-e2erow${process.pid}`;
    const out = refuse(prefix, [
      `${prefix}-nats-${process.pid}-fedcba9876543210|`,
      `${prefix}-pg-${deadPid()}|{"owner":null}`,
    ]);
    expect(out).toBe("passed\n");
  });
});
