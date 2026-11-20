import { afterEach, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { skipWithout } from "@legion/test-fixtures/host-tools";

// scripts/smoke-clear-rebind.sh's channel_server_of: Claude's channel server is the child of the
// Claude process whose command line runs dist/envoy-channel.js. It reads that process's own
// children (proc_children, scripts/e2e/lib/process-tree.sh) and each child's command line, never
// every process of the machine, so neither a grandchild nor a process outside the tree is the
// server, however it looks. The smoke itself needs a real Claude Code; the function is read from it
// by name and run on real processes here. Each stand-in names the server's file through $M, so no
// command line but a server's carries it, the stand-ins' own and their children's before they exec
// included.
const smoke = join(import.meta.dir, "..", "scripts", "smoke-clear-rebind.sh");
const processTree = join(import.meta.dir, "..", "..", "..", "scripts", "e2e", "lib", "process-tree.sh");
const skip = skipWithout("bash", "sleep", "sed");
const serverFile = "dist/envoy-channel.js";
const started: number[] = [];
afterEach(() => {
  for (const pid of started.splice(0)) {
    try {
      process.kill(-pid, "SIGKILL");
    } catch {}
  }
});

// stand spawns BODY as a bash leading a session and a process group of its own (its group is its
// pid, so the row ends all it started), and answers its pid and what it printed once it has printed
// `ready`. Nothing here stops a process, so its group's having no parent in this session is moot.
async function stand(body: string): Promise<{ pid: number; said: string }> {
  const child = Bun.spawn(["bash", "-c", `${body}\necho ready\nwait`], {
    detached: true,
    env: { PATH: process.env["PATH"] ?? "", M: serverFile },
    stdout: "pipe",
    stderr: "ignore",
  });
  started.push(child.pid);
  const reader = child.stdout.getReader();
  let said = "";
  while (!said.includes("ready\n")) {
    const { value, done } = await reader.read();
    if (done) throw new Error(`the stand-in ended early: ${said}`);
    said += new TextDecoder().decode(value);
  }
  reader.releaseLock();
  return { pid: child.pid, said };
}

// The exec of each stand-in server is the kernel's, which no fake clock drives: its own command
// line is read until it is the server's, 100 reads 20 ms apart.
async function untilServing(pid: number) {
  for (let i = 0; i < 100; i++) {
    try {
      if (readFileSync(`/proc/${pid}/cmdline`, "utf8").includes(serverFile)) return;
    } catch {}
    await Bun.sleep(20);
  }
  throw new Error(`pid ${pid} never ran ${serverFile}`);
}

function channelServerOf(pid: number) {
  const ran = Bun.spawnSync([
    "bash",
    "-c",
    `. "$1"\neval "$(sed -n '/^channel_server_of() {/,/^}/p' "$2")"\nchannel_server_of "$3"; echo "status $?"`,
    "-",
    processTree,
    smoke,
    String(pid),
  ]);
  return { out: ran.stdout.toString(), err: ran.stderr.toString() };
}

const server = `bash -c 'sleep 60; :' x "$M" </dev/null >/dev/null 2>&1 &`;

test.skipIf(skip)(
  "names Claude's child that runs the channel server, and neither a grandchild that does nor a process outside",
  async () => {
    const outside = await stand(`${server}\necho "outside $!"`);
    const claude = await stand(`sleep 60 </dev/null >/dev/null 2>&1 &\n${server}\necho "server $!"`);
    // The middle shell, the stand-in's child, says its own child's pid before the stand-in says
    // ready: it runs in a process substitution the stand-in reads one line of.
    const deeper = await stand(
      `exec 3< <(bash -c 'bash -c "sleep 60; :" x "$M" </dev/null >/dev/null 2>&1 & echo "grandchild $!"; wait')\nread -r line <&3\necho "$line"`
    );
    const outsider = Number(/outside (\d+)/.exec(outside.said)?.[1]);
    const child = Number(/server (\d+)/.exec(claude.said)?.[1]);
    const grandchild = Number(/grandchild (\d+)/.exec(deeper.said)?.[1]);
    for (const pid of [outsider, child, grandchild]) await untilServing(pid);
    expect(channelServerOf(claude.pid)).toEqual({ out: `${child}\nstatus 0\n`, err: "" });
    expect(channelServerOf(deeper.pid)).toEqual({ out: "status 1\n", err: "" });
    // The control: the process outside is a server to its own parent, the stand-in that started it.
    expect(channelServerOf(outside.pid)).toEqual({ out: `${outsider}\nstatus 0\n`, err: "" });
  }
);
