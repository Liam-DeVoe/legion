import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// The listener and the Legion daemon refuse a nats-server below the floor packages/envoy/natsversion
// holds (every 2.10 among them), so a live proof that starts its own NATS server must start one the
// floor takes, or its stack never comes up. Every such driver starts the image lib/nats-image.sh
// names, which it reads from the one constant the Go tests run Envoy on, testnats.HubImage
// (packages/envoy/internal/testnats/nats.go), the hub's nats:2.15.1-alpine.

const root = join(import.meta.dir, "..", "..", "..");

/** testnats.HubImage as packages/envoy/internal/testnats/nats.go declares it. */
function hubImage(): string {
  const source = readFileSync(join(root, "packages/envoy/internal/testnats/nats.go"), "utf8");
  const found = /^const HubImage = "([^"]+)"$/m.exec(source);
  if (found?.[1] === undefined)
    throw new Error("packages/envoy/internal/testnats/nats.go declares no HubImage");
  return found[1];
}

/** The drivers that start a NATS server of their own for the listener or the daemon to use. */
const drivers = [
  "scripts/e2e/stage3-devbox-workflow.sh",
  "scripts/e2e/claude-backend-tmux.sh",
  "scripts/e2e/claude-backend-sandbox.sh",
  "scripts/e2e/controller-start-tmux.sh",
  "scripts/e2e/dispatch-user-turns.sh",
  "scripts/e2e/parked-decision-devbox.sh",
  "scripts/e2e/stage2-tmux-supervision.sh",
  "scripts/e2e/verifiers-staging-token.sh",
  "docs/site/media/walkthroughs/legion-operator/record-casts.sh",
  "packages/pi-legion/scripts/skill-scenarios/rig.sh",
];

/**
 * Each problem a driver's text has: a `docker run` that starts a NATS server (its arguments end in
 * `-js`, as a JetStream server's do) on any image but `"$nats_image"`, and a driver that starts one
 * and never sources lib/nats-image.sh, which sets that variable.
 */
function natsProblems(name: string, text: string): string[] {
  const problems: string[] = [];
  const lines = text.split("\n");
  let starts = 0;
  lines.forEach((line, index) => {
    if (!/\bdocker run\b/.test(line) || !/\s-js\b/.test(line)) return;
    starts++;
    if (!line.includes('"$nats_image"')) {
      problems.push(
        `${name}:${index + 1} starts NATS on an image other than "$nats_image": ${line.trim()}`
      );
    }
  });
  if (starts > 0 && !/^\s*\. "\$root\/scripts\/e2e\/lib\/nats-image\.sh"/m.test(text)) {
    problems.push(`${name} starts NATS and never sources "$root/scripts/e2e/lib/nats-image.sh"`);
  }
  if (starts === 0) problems.push(`${name} starts no NATS server, so this test no longer reads it`);
  return problems;
}

describe("lib/nats-image.sh", () => {
  test("names testnats.HubImage, the image the Go tests run Envoy on", () => {
    const result = Bun.spawnSync(
      [
        "bash",
        "-c",
        `root=${JSON.stringify(root)}; . "$root/scripts/e2e/lib/nats-image.sh"; printf %s "$nats_image"`,
      ],
      { stderr: "pipe" }
    );
    expect(result.exitCode, result.stderr.toString()).toBe(0);
    expect(result.stdout.toString()).toBe(hubImage());
  });

  test("ends the driver when it cannot read the constant", () => {
    const helper = join(root, "scripts/e2e/lib/nats-image.sh");
    const result = Bun.spawnSync(
      ["bash", "-c", `root=/nonexistent; . ${JSON.stringify(helper)}; echo reached`],
      {
        stderr: "pipe",
      }
    );
    expect(result.exitCode).not.toBe(0);
    expect(result.stdout.toString()).not.toContain("reached");
    expect(result.stderr.toString()).toContain("HubImage");
  });
});

describe("every live proof starts the NATS server the floor takes", () => {
  // The control the check below leans on: the line every driver had before the floor is found.
  test("a driver starting nats:2.10 is named", () => {
    const old = `. "$root/scripts/e2e/lib/nats-image.sh"\ndocker run -d --name "$nats_container" -p 127.0.0.1::4222 nats:2.10 -js >/dev/null\n`;
    expect(natsProblems("fixture.sh", old)).toEqual([
      'fixture.sh:2 starts NATS on an image other than "$nats_image": docker run -d --name "$nats_container" -p 127.0.0.1::4222 nats:2.10 -js >/dev/null',
    ]);
  });

  for (const driver of drivers) {
    test(driver, () => {
      expect(natsProblems(driver, readFileSync(join(root, driver), "utf8"))).toEqual([]);
    });
  }
});
