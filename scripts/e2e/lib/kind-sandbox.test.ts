import { describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// kind_sandbox_create (kind-sandbox.sh) asks kind for the cluster again when kind fails on a host
// port bind. kind picks the API server's host port as one that is free when it looks, and on a busy
// host another socket can take that port before docker binds it: a run of controller-sweep-kind.sh
// on 2026-11-20 failed at its `cluster` check with `failed to bind host port 127.0.0.1:46243/tcp:
// address already in use`, before any of the proof ran. Here a stand-in kind fails its first creates
// that way, and a stand-in kubectl answers the node reads that follow a create.
const work = temporaryDirectory("kind-sandbox");
const lib = join(import.meta.dir, "kind-sandbox.sh");

const fakeKind = `#!/bin/sh
echo "$*" >>"$KIND_CALLS"
case "$1" in
create)
  n=$(grep -c '^create' "$KIND_CALLS")
  if [ "$n" -le "\${KIND_ALLOCATED_FAILURES:-0}" ]; then
    echo "docker: Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint legion-test-control-plane (2c682f77): Bind for 127.0.0.1:3565$n failed: port is already allocated" >&2
    exit 1
  fi
  if [ "$n" -le "\${KIND_BIND_FAILURES:-0}" ]; then
    echo "docker: Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint legion-test-control-plane (1676b995): failed to bind host port 127.0.0.1:4624$n/tcp: address already in use" >&2
    echo 'ERROR: failed to create cluster: command "docker run --name legion-test-control-plane" failed with error: exit status 125' >&2
    exit 1
  fi
  if [ -n "\${KIND_OTHER_FAILURE:-}" ]; then
    echo 'ERROR: failed to create cluster: node(s) already exist for a cluster with the name "legion-test"' >&2
    exit 1
  fi
  ;;
version) echo 'kind v0.33.0 go1.26.7 linux/amd64' ;;
esac
`;

const fakeKubectl = `#!/bin/sh
echo "kubectl $*" >>"$KIND_CALLS"
case "$*" in *jsonpath*) echo 'v1.37.0 8 cpu 16Gi memory' ;; esac
`;

/** One run of kind_sandbox_create over the stand-ins: its exit, what it printed, and kind's calls. */
interface Created {
  readonly status: number;
  readonly output: string;
  readonly kind: string[];
  readonly createCall: string;
  readonly deleteCall: string;
}

let runs = 0;

function create(env: Record<string, string>): Created {
  const dir = join(work, `run-${++runs}`);
  const bin = join(dir, "bin");
  mkdirSync(bin, { recursive: true });
  for (const [name, body] of [
    ["kind", fakeKind],
    ["kubectl", fakeKubectl],
  ] as const) {
    writeFileSync(join(bin, name), body);
    chmodSync(join(bin, name), 0o755);
  }
  const calls = join(dir, "calls");
  writeFileSync(calls, "");
  const result = Bun.spawnSync(
    [
      "bash",
      "-c",
      `set -euo pipefail
note() { echo "   $*"; }
fail() { echo "FAIL $*" >&2; exit 1; }
. "$LIB"
kind_sandbox_create legion-test "$WORK"
echo created`,
    ],
    {
      env: {
        PATH: `${bin}:${process.env.PATH ?? ""}`,
        LEGION_E2E_KIND: join(bin, "kind"),
        KIND_CALLS: calls,
        LIB: lib,
        WORK: dir,
        ...env,
      },
    }
  );
  return {
    status: result.exitCode,
    output: result.stdout.toString() + result.stderr.toString(),
    kind: readFileSync(calls, "utf8")
      .split("\n")
      .filter((line) => line !== "" && !line.startsWith("kubectl ")),
    createCall: `create cluster --name legion-test --kubeconfig ${dir}/kubeconfig --wait 180s`,
    deleteCall: `delete cluster --name legion-test --kubeconfig ${dir}/kubeconfig`,
  };
}

describe("kind_sandbox_create", () => {
  test("a create kind fails on a host port bind is made again, after the cluster is deleted by its name, and the retry is named", () => {
    const created = create({ KIND_BIND_FAILURES: "1" });
    expect(created.status).toBe(0);
    expect(created.kind).toEqual([
      created.createCall,
      created.deleteCall,
      created.createCall,
      "version",
    ]);
    expect(created.output).toContain(
      "   kind create cluster try 1 of 3 failed on a host port another socket took (docker: Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint legion-test-control-plane (1676b995): failed to bind host port 127.0.0.1:46241/tcp: address already in use); deleting cluster legion-test and trying again"
    );
    expect(created.output).toContain("created");
  });

  test("three creates that each fail on a host port bind fail the run, naming the last bind", () => {
    const created = create({ KIND_BIND_FAILURES: "3" });
    expect(created.status).toBe(1);
    expect(created.kind).toEqual([
      created.createCall,
      created.deleteCall,
      created.createCall,
      created.deleteCall,
      created.createCall,
    ]);
    expect(created.output).toContain(
      "kind create cluster try 2 of 3 failed on a host port another socket took"
    );
    expect(created.output).toContain(
      "FAIL kind could not create cluster legion-test in 3 tries, each failing on a host port bind; the last: docker: Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint legion-test-control-plane (1676b995): failed to bind host port 127.0.0.1:46243/tcp: address already in use"
    );
    expect(created.output).not.toContain("created\n");
  });

  test("a create docker refuses because another container holds the port is made again too", () => {
    const created = create({ KIND_ALLOCATED_FAILURES: "1" });
    expect(created.status).toBe(0);
    expect(created.kind).toEqual([
      created.createCall,
      created.deleteCall,
      created.createCall,
      "version",
    ]);
    expect(created.output).toContain(
      "   kind create cluster try 1 of 3 failed on a host port another socket took (docker: Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint legion-test-control-plane (2c682f77): Bind for 127.0.0.1:35651 failed: port is already allocated); deleting cluster legion-test and trying again"
    );
  });

  test("a create that fails for any other reason is not made again, and the run fails naming kind's last line", () => {
    const created = create({ KIND_OTHER_FAILURE: "1" });
    expect(created.status).toBe(1);
    expect(created.kind).toEqual([created.createCall]);
    expect(created.output).toContain(
      'FAIL kind could not create cluster legion-test: ERROR: failed to create cluster: node(s) already exist for a cluster with the name "legion-test"'
    );
    expect(created.output).not.toContain("trying again");
  });
});
