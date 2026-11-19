import { afterEach, expect, test } from "bun:test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { skipWithout } from "@legion/test-fixtures/host-tools";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// The stand-in asks the daemon's own workflow for the phase its role works, which `go run`
// compiles and runs, so a host with no go runs none of these rows.
const skip = skipWithout("go");

const rig = temporaryDirectory("skill-scenarios-daemon-standin");
const standin = path.join(import.meta.dir, "daemon-standin.ts");
const started: Bun.Subprocess[] = [];
afterEach(() => {
  for (const child of started.splice(0)) child.kill();
});

/** One stand-in for `role` of issue LWEVAL-1, and the base URL it listens on. */
async function startStandin(role: string): Promise<string> {
  const dir = path.join(rig, `${role}-${started.length}`);
  const boot = `${dir}-boot`;
  writeFileSync(boot, "rig-boot-token\n");
  const child = Bun.spawn(
    ["bun", standin, "0", `${dir}-daemon.log`, boot, "skillsrig", "LWEVAL-1", role],
    { stdout: "pipe", stderr: "inherit" }
  );
  started.push(child);
  const reader = child.stdout.getReader();
  let seen = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) throw new Error(`the ${role} stand-in ended before it listened: ${seen}`);
    seen += new TextDecoder().decode(value);
    const listening = seen.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/);
    if (listening?.[1] !== undefined) return listening[1];
  }
}

/** A completion of the stand-in's phase, under a grant it minted, naming a handoff record. */
async function complete(base: string, verdict: string) {
  const post = (route: string, body: unknown) =>
    fetch(`${base}${route}`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
  const grant = await post("/legion/v1/grants", {
    sessionId: "rig-session",
    secret: "rig-secret",
    tree: "LWEVAL-1",
    issue: "LWEVAL-1",
  });
  expect(grant.status).toBe(200);
  const { grantId } = (await grant.json()) as { grantId: string };
  const done = await post("/legion/v1/handoff/complete", {
    grantId,
    summary: "the phase is done",
    verdict,
    handoffId: "rig-record",
    handoffVersion: 1,
  });
  return { status: done.status, body: (await done.json()) as Record<string, unknown> };
}

// Catches: a stand-in that takes a tester's completion with no verdict, which the daemon refuses
// (`TESTER_VERDICT_REQUIRED`, packages/daemon/internal/api/handoff.go), so a run whose tester
// forgot the verdict is accepted and scored as the role's failure, where a daemon would have
// refused it and the role could have completed again (INFRA-1303: c-head-2, f-head-3 and
// l-head-3 of the batch of record at 6a643564). The daemon's go run can take minutes on a cold
// build cache, as daemon-pane.test.ts says of the same program.
test.skipIf(skip)(
  "a tester's completion that names no verdict is refused as the daemon refuses it",
  async () => {
    const base = await startStandin("tester");
    expect(await complete(base, "")).toEqual({
      status: 400,
      body: { code: "TESTER_VERDICT_REQUIRED", error: "tester verdict must be pass or fail" },
    });
    expect((await complete(base, "pass")).status).toBe(200);
  },
  300_000
);

// Catches: a stand-in that takes a verdict from a role that is not the tester, which the daemon
// refuses (`VERDICT_ROLE_FORBIDDEN`, the same file), so a reviewer's run that sends one is
// scored on a completion no daemon would have taken.
test.skipIf(skip)(
  "a completion that names a verdict from a role other than the tester is refused",
  async () => {
    const base = await startStandin("reviewer");
    expect(await complete(base, "pass")).toEqual({
      status: 400,
      body: { code: "VERDICT_ROLE_FORBIDDEN", error: "only a tester may report a verdict" },
    });
    expect((await complete(base, "")).status).toBe(200);
  },
  300_000
);
