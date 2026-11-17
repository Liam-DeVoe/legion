/** Red test for PR #4236, acceptance finding D3. Goes at
 *  scripts/e2e/lib/released-port-probe.test.ts.
 *
 *  R1 of PR #4236: "A test stack starts each server of this repository on a socket that the stack
 *  holds. It holds the socket from the choice of the port until the server serves on it." R2:
 *  "For a program that cannot take a socket, the program binds a port that the kernel chooses.
 *  The stack reads the port from the program: from its output, or from the kernel's list of the
 *  sockets that the process holds."
 *
 *  A command substitution that asks Python for a port, prints it and closes the socket hands the
 *  caller a port nothing holds: another program can take it before the server the caller starts on
 *  it binds it, which is the fault the issue removes. At 919bdcbef8,
 *  packages/envoy/scripts/airlock-check.sh does that three times: k4's .svc stand-in (line 797)
 *  and k7's broker, its API and its proxy port (lines 1359-1360). The broker takes port 0 for each
 *  of its addresses and logs what it bound (packages/envoy/scripts/dev-airlock.sh, "a port the
 *  system picks (BROKER_*_LISTEN_ADDR of port 0)"); `python3 -m http.server 0` prints the port it
 *  serves.
 *
 *  The row reads every tracked file and fails on each line that prints the port of a socket it
 *  closes in the same probe. */

import { expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const root = execFileSync("git", ["rev-parse", "--show-toplevel"], {
  cwd: import.meta.dir,
  encoding: "utf8",
}).trim();

// One probe: a socket bound to port 0, its port printed, and the socket closed, in one Python
// program on one line, whatever the variable is called.
const releasedProbe =
  /bind\(\(\s*["'][^"']*["']\s*,\s*0\s*\)\)[^\n]*getsockname\(\)\[1\][^\n]*\.close\(\)/;

test("no tracked file hands a server a port from a probe socket it has already closed", () => {
  const files = execFileSync("git", ["ls-files", "-z"], { cwd: root, encoding: "utf8" })
    .split("\0")
    .filter((file) => file !== "" && !file.endsWith("released-port-probe.test.ts"));
  const hits: string[] = [];
  for (const file of files) {
    let text: string;
    try {
      text = readFileSync(join(root, file), "utf8");
    } catch {
      continue;
    }
    if (!text.includes("getsockname")) continue;
    text.split("\n").forEach((line, index) => {
      if (releasedProbe.test(line)) hits.push(`${file}:${index + 1}`);
    });
  }
  expect(hits).toEqual([]);
});
