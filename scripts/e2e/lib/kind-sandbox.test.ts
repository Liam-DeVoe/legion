import { describe, expect, test } from "bun:test";
import { join } from "node:path";

// lib/kind-sandbox.sh's kind_node_pulls, the list of images a kind node pulled that
// kind_sandbox_teardown reads from each node's containerd journal and holds to
// ghcr.io/cybermonklabs/legion/ci by digest and the rig's own registry (CHECK kind-node-pulls).
// Each case is a journal of testdata/kind-node-pulls, whose README says how a run's journal is
// captured and how these were written.
const library = join(import.meta.dir, "kind-sandbox.sh");
const journals = join(import.meta.dir, "testdata", "kind-node-pulls");
const node = "legion-rig-4242-control-plane";

/** The reader as the teardown calls it: sourced, with the note every caller defines, over one
 *  node's journal ($2). */
const reader = `. "$1"
note() { printf '%s\\n' "$*"; }
kind_node_pulls "$2"
`;

/** The reader over the journal of one case: its exit status and what it printed. */
function pulls(name: string): { code: number; lines: string[] } {
  const journal = join(journals, name, `containerd-${node}.journal`);
  const run = Bun.spawnSync(["bash", "-c", reader, "kind-node-pulls", library, journal], {
    stderr: "pipe",
    stdout: "pipe",
  });
  return { code: run.exitCode, lines: `${run.stdout}${run.stderr}`.trim().split("\n") };
}

describe("kind_node_pulls", () => {
  test("pulls of ghcr.io/cybermonklabs/legion/ci by digest and of the rig's registry pass, and a refused pull is none", () => {
    const { code, lines } = pulls("pinned-and-local");
    expect(lines).toEqual([
      `the node ${node} pulled ghcr.io/cybermonklabs/legion/ci@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec`,
      `the node ${node} pulled ghcr.io/cybermonklabs/legion/ci@sha256:c825f3d5e28bde099bd7a3daace28772d412c9157ad47fa752a9ad0baafc118d`,
      `the node ${node} pulled localhost:41234/legion-worker-stand-ins@sha256:3f0c9a1d7be4e2c55a86d0b1e9f4a7c2d8e6b5a4f3e2d1c0b9a8f7e6d5c4b3a2 from the rig's registry`,
      "CHECK kind-node-pulls: PASS: each of the 3 images the nodes pulled is ghcr.io/cybermonklabs/legion/ci by digest or the rig's registry",
    ]);
    expect(code).toBe(0);
  });

  test("a pull of ghcr.io in another repository fails, naming it", () => {
    const { code, lines } = pulls("outside-repository");
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
    expect(code).toBe(3);
  });
});
