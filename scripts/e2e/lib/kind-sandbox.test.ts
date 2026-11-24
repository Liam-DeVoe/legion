import { describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

// lib/kind-sandbox.sh's kind_node_pulls, the list of images a kind node pulled that
// kind_sandbox_teardown reads from each node's containerd journal and holds to
// ghcr.io/cybermonklabs/legion/ci by digest and the rig's own registry (CHECK kind-node-pulls).
// Each case is a journal of testdata/kind-node-pulls, whose README says which run each was
// captured from and how.
const library = join(import.meta.dir, "kind-sandbox.sh");
const journals = join(import.meta.dir, "testdata", "kind-node-pulls");

/** The reader as the teardown calls it: sourced, with the note every caller defines, over one
 *  node's journal ($2). */
const reader = `. "$1"
note() { printf '%s\\n' "$*"; }
kind_node_pulls "$2"
`;

/** The reader over the one journal of a case: the node its file names, its exit status and what
 *  it printed. */
function pulls(name: string): { node: string; code: number; lines: string[] } {
  const [file] = readdirSync(join(journals, name)).filter((entry) => entry.endsWith(".journal"));
  const node = file.replace(/^containerd-/, "").replace(/\.journal$/, "");
  const run = Bun.spawnSync(
    ["bash", "-c", reader, "kind-node-pulls", library, join(journals, name, file)],
    { stderr: "pipe", stdout: "pipe" }
  );
  return { node, code: run.exitCode, lines: `${run.stdout}${run.stderr}`.trim().split("\n") };
}

describe("kind_node_pulls", () => {
  test("pulls of ghcr.io/cybermonklabs/legion/ci by digest and of the rig's registry pass", () => {
    const { node, code, lines } = pulls("pinned-and-local");
    expect(lines).toEqual([
      `the node ${node} pulled ghcr.io/cybermonklabs/legion/ci@sha256:8c8f5814c16bd68631af0496a5fa4eb9bedce4d032de88b956130a041d9f438e`,
      `the node ${node} pulled ghcr.io/cybermonklabs/legion/ci@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec`,
      `the node ${node} pulled ghcr.io/cybermonklabs/legion/ci@sha256:c825f3d5e28bde099bd7a3daace28772d412c9157ad47fa752a9ad0baafc118d`,
      `the node ${node} pulled localhost:44555/legion-worker-stand-ins@sha256:a5f28c8038ec55bb42c6d836f7fc80dcd336c020767a6f0844c65821a42533db from the rig's registry`,
      "CHECK kind-node-pulls: PASS: each of the 4 images the nodes pulled is ghcr.io/cybermonklabs/legion/ci by digest or the rig's registry",
    ]);
    expect(code).toBe(0);
  });

  test("a pull of another registry that containerd refused is no pull", () => {
    const { node, code, lines } = pulls("refused-pull");
    expect(lines).toEqual([
      `the node ${node} pulled ghcr.io/cybermonklabs/legion/ci@sha256:8c8f5814c16bd68631af0496a5fa4eb9bedce4d032de88b956130a041d9f438e`,
      "CHECK kind-node-pulls: PASS: each of the 1 images the nodes pulled is ghcr.io/cybermonklabs/legion/ci by digest or the rig's registry",
    ]);
    expect(code).toBe(0);
  });

  test("a pull of ghcr.io in another repository fails, naming it", () => {
    const { node, code, lines } = pulls("outside-repository");
    expect(lines).toContain(
      `the node ${node} pulled ghcr.io/containerd/busybox:1.36, which is neither ghcr.io/cybermonklabs/legion/ci by digest nor the rig's registry`
    );
    expect(lines.at(-1)).toStartWith(
      "CHECK kind-node-pulls: FAIL: a node pulled an image from outside"
    );
    expect(code).toBe(3);
  });

  test("a journal with no PullImage line fails rather than passes", () => {
    const { code, lines } = pulls("no-pull");
    expect(lines).toHaveLength(1);
    expect(lines[0]).toStartWith("CHECK kind-node-pulls: FAIL: no containerd journal of the nodes");
    expect(code).toBe(4);
  });
});

// kind_sandbox_teardown over a cluster whose node left no journal to read, as a rig that failed
// before its first pull leaves it: kind, kubectl and docker are stand-ins that list the cluster,
// no node, and do nothing else. A test that failed already (STATUS 1) gets no second failure from
// the empty journal list; a test that passed (STATUS 0), the control, fails on it with status 3.
const scratch = temporaryDirectories("kind-sandbox-teardown");

/** The teardown as the rig calls it, after kind_sandbox_use, over the stand-ins: its exit status
 *  and what it printed. */
function teardown(status: number): { code: number; out: string } {
  const dir = scratch("rig");
  const bin = join(dir, "bin");
  mkdirSync(bin);
  const standIn = (name: string, body: string) => {
    writeFileSync(join(bin, name), `#!/usr/bin/env bash\n${body}\n`);
    chmodSync(join(bin, name), 0o755);
  };
  standIn("kind", '[ "$1 $2" != "get clusters" ] || echo rig\nexit 0');
  standIn("kubectl", "exit 1");
  standIn("docker", "exit 0");
  const script = `. "$1"
note() { printf '%s\\n' "$*"; }
fail() { printf 'FAIL: %s\\n' "$*"; exit 1; }
kind_sandbox_use rig "$2"
kind_sandbox_teardown "$3" "$2/evidence"`;
  const run = Bun.spawnSync(["bash", "-c", script, "kind-rig", library, dir, String(status)], {
    env: {
      ...process.env,
      PATH: `${bin}:${process.env.PATH ?? ""}`,
      LEGION_E2E_KIND: join(bin, "kind"),
    },
    stderr: "pipe",
    stdout: "pipe",
  });
  return { code: run.exitCode, out: `${run.stdout}${run.stderr}` };
}

describe("kind_sandbox_teardown", () => {
  test("a journal with no pull is no second failure of a test that failed already, and fails a test that passed", () => {
    const failed = teardown(1);
    expect({ code: failed.code, out: failed.out }).toMatchObject({ code: 0 });
    expect(failed.out).toContain("CHECK kind-node-pulls: FAIL: no containerd journal of the nodes");
    expect(failed.out).toContain(
      "the test failed already (status 1), so a journal with no pull is no second failure"
    );
    const passed = teardown(0);
    expect({ code: passed.code, out: passed.out }).toMatchObject({ code: 3 });
    expect(passed.out).not.toContain("no second failure");
  });
});
