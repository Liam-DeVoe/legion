import { afterEach, expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { skipWithout } from "@legion/test-fixtures/host-tools";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { ownGroup } from "./own-group";

// lib/codex-host.sh's codex_server_pid under --real-hosts: the plugin server of one Codex session
// is the process below that session's tmux pane whose command line runs the installed
// envoy-channel.js for Codex and whose working directory is in that session's Codex home. It reads
// the pane's tree from each process's own /proc entry (proc_children), never every process of the
// machine, so a process outside the pane's tree is no server of it, however it looks. A stand-in
// tmux names the pane, a real bash here, which starts a server below a shell of its own and a decoy
// of another session's home beside it; a second decoy, the same server in the same home, runs
// outside the pane. The pane's shells name the server's file through $M, so no command line but a
// server's carries it.
const codexHost = join(import.meta.dir, "codex-host.sh");
const dir = temporaryDirectory("codex-host-test");
// The Codex home of the stand-in sessions, read back as the kernel names a working directory.
const codexRoot = temporaryDirectory("codex-host-root", { real: true });
// perl: each process here starts through ownGroup (lib/own-group.ts), so its group can be ended.
const skip = skipWithout("perl", "bash", "sleep");
const marker = ["dist/envoy-channel.js", "--host", "codex"];
const serves = ["bash", "-c", "sleep 60; :", "x", ...marker];
const groups: number[] = [];
afterEach(() => {
  for (const group of groups.splice(0)) {
    try {
      process.kill(-group, "SIGKILL");
    } catch {}
  }
});

// The exec of each stand-in is the kernel's, which no fake clock drives: each one's own command
// line is read until it carries the marker, 100 reads 20 ms apart.
async function untilServing(pid: number) {
  for (let i = 0; i < 100; i++) {
    try {
      if (readFileSync(`/proc/${pid}/cmdline`, "utf8").includes(marker.join("\0"))) return;
    } catch {}
    await Bun.sleep(20);
  }
  throw new Error(`pid ${pid} never ran ${marker.join(" ")}`);
}

// serverPid runs codex_server_pid LABEL with hosts=real over the Codex home ROOT, with a tmux whose
// display-message names PANE as the session's pane.
function serverPid(root: string, label: string, pane: number) {
  const bin = join(dir, "bin");
  mkdirSync(bin, { recursive: true });
  writeFileSync(join(bin, "tmux"), '#!/bin/sh\necho "$PANE"\n', { mode: 0o755 });
  const ran = Bun.spawnSync(
    [
      "bash",
      "-c",
      `hosts=real codex_root=$1 codex_tmux_dir=$1/tmux tmux_bin=$2/tmux\n. ${JSON.stringify(codexHost)}\ncodex_server_pid "$3"; echo "status $?"`,
      "-",
      root,
      bin,
      label,
    ],
    { env: { PATH: process.env.PATH ?? "", PANE: String(pane) } }
  );
  return { out: ran.stdout.toString(), err: ran.stderr.toString() };
}

test.skipIf(skip)(
  "names the session's server below its pane, and neither another session's below it nor the same one outside it",
  async () => {
    const root = codexRoot;
    const home = join(root, "a", "cwd");
    const other = join(root, "b", "cwd");
    mkdirSync(home, { recursive: true });
    mkdirSync(other, { recursive: true });
    // Outside the pane: the same command line in the same home, which a walk of every process
    // would name as well as the one below the pane.
    const outside = Bun.spawn(ownGroup(serves), { cwd: home, stdout: "ignore", stderr: "ignore" });
    groups.push(outside.pid);
    const pane = Bun.spawn(
      ownGroup([
        "bash",
        "-c",
        `(cd "$2" && exec bash -c 'sleep 60; :' x "$M" --host codex) </dev/null >/dev/null 2>&1 &
echo "decoy $!"
bash -c '(cd "$1" && exec bash -c "sleep 60; :" x "$M" --host codex) </dev/null >/dev/null 2>&1 & echo "server $!"; wait' - "$1" &
wait`,
        "-",
        home,
        other,
      ]),
      { env: { PATH: process.env.PATH ?? "", M: marker[0] }, stdout: "pipe", stderr: "ignore" }
    );
    groups.push(pane.pid);
    const reader = pane.stdout.getReader();
    let said = "";
    while (!/server \d+\n/.test(said) || !/decoy \d+\n/.test(said)) {
      const { value, done } = await reader.read();
      if (done) throw new Error(`the stand-in pane ended early: ${said}`);
      said += new TextDecoder().decode(value);
    }
    const server = Number(/server (\d+)/.exec(said)?.[1]);
    const decoy = Number(/decoy (\d+)/.exec(said)?.[1]);
    for (const pid of [outside.pid, server, decoy]) await untilServing(pid);
    expect(serverPid(root, "a", pane.pid)).toEqual({ out: `${server}\nstatus 0\n`, err: "" });
    // The other session's home holds the decoy below the pane.
    expect(serverPid(root, "b", pane.pid)).toEqual({ out: `${decoy}\nstatus 0\n`, err: "" });
    // The control: the process outside is a server of session a to a pane that is its own.
    expect(serverPid(root, "a", outside.pid)).toEqual({
      out: `${outside.pid}\nstatus 0\n`,
      err: "",
    });
    expect(serverPid(root, "c", pane.pid)).toEqual({ out: "status 1\n", err: "" });
  }
);
