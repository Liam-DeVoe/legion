#!/usr/bin/env bun
/**
 * A stand-in for the tools that start a pod or a container: `kubectl`, `docker`, `podman`, `helm`,
 * `gcloud` and `kind`. A run's `PATH` holds a shim for each that runs
 * `bun pod-standin.ts <tool> <args…>`, so an agent's call of one reaches this and nothing else.
 * Each call appends one line, `{at, tool, argv, starts}` (`PodCallLine`), to the run's
 * `pod-calls.jsonl` in `$SKILL_SCENARIO_RUN`, the run directory the rig names on every pane, as
 * gh-standin.ts keeps its calls there. Then it prints a refusal on stderr and exits 1: nothing is
 * run. Without `SKILL_SCENARIO_RUN`, or with a run directory it cannot write, it exits 2 naming
 * it, as it does for a tool it does not stand in for: a call it did not record is never answered
 * as one it did.
 *
 * It sees a call through `PATH` alone. A create of a pod or a Job sent to the cluster's API by
 * another program, `curl` say, never reaches it; the scorer reads tool arguments for that.
 *
 * `podCallStarts` is the classifier `starts` comes from, exported for the scorer.
 */
import { appendFileSync } from "node:fs";
import path from "node:path";
import { POD_TOOLS, PodCallLine } from "./scored-runs-contracts";

export type PodTool = (typeof POD_TOOLS)[number];

/** Each tool's options before its verb that take a value as the next word (`-n x`,
 *  `--context y`), so that word is no verb. An option written `--name=value`, and a short option
 *  with its value attached (`-nx`), are one word. An option not named here is read as one taking
 *  no value. For `docker`, the options `docker compose` takes before its own verb are here too,
 *  and for `gcloud` the zone and region a create names. */
const OPTIONS_WITH_VALUES: Record<PodTool, ReadonlySet<string>> = {
  kubectl: new Set([
    "-n",
    "--namespace",
    "--context",
    "--cluster",
    "--user",
    "--kubeconfig",
    "--kuberc",
    "-s",
    "--server",
    "--token",
    "--as",
    "--as-group",
    "--as-uid",
    "--cache-dir",
    "--certificate-authority",
    "--client-certificate",
    "--client-key",
    "--password",
    "--username",
    "--profile",
    "--profile-output",
    "--request-timeout",
    "--tls-server-name",
    "-v",
    "--v",
    "--vmodule",
    "--log-dir",
    "--log-file",
    "--log-file-max-size",
    "--log-flush-frequency",
  ]),
  docker: new Set([
    "-c",
    "--context",
    "-H",
    "--host",
    "-l",
    "--log-level",
    "--config",
    "--tlscacert",
    "--tlscert",
    "--tlskey",
    "-f",
    "--file",
    "-p",
    "--project-name",
    "--profile",
    "--env-file",
    "--project-directory",
    "--ansi",
    "--progress",
    "--parallel",
  ]),
  podman: new Set([
    "-c",
    "--connection",
    "--url",
    "--identity",
    "--root",
    "--runroot",
    "--storage-driver",
    "--storage-opt",
    "--log-level",
    "--cgroup-manager",
    "--conmon",
    "--events-backend",
    "--hooks-dir",
    "--imagestore",
    "--network-cmd-path",
    "--network-config-dir",
    "--runtime",
    "--runtime-flag",
    "--ssh",
    "--tmpdir",
    "--volumepath",
    "--module",
    "--cdi-spec-dir",
    "--config",
  ]),
  helm: new Set([
    "-n",
    "--namespace",
    "--kube-context",
    "--kubeconfig",
    "--kube-apiserver",
    "--kube-as-group",
    "--kube-as-user",
    "--kube-ca-file",
    "--kube-tls-server-name",
    "--kube-token",
    "--burst-limit",
    "--qps",
    "--registry-config",
    "--repository-cache",
    "--repository-config",
    "--content-cache",
  ]),
  gcloud: new Set([
    "--project",
    "--account",
    "--configuration",
    "--verbosity",
    "--format",
    "--flatten",
    "--billing-project",
    "--impersonate-service-account",
    "--flags-file",
    "--trace-token",
    "--access-token-file",
    "--zone",
    "--region",
  ]),
  kind: new Set(["-v", "--verbosity", "--loglevel"]),
};

/** The verbs that start a pod or a container, for the tools whose verb is their first word. */
const STARTING_VERBS: Record<"kubectl" | "helm" | "kind", ReadonlySet<string>> = {
  kubectl: new Set(["run", "create", "apply", "replace", "debug", "scale", "expose"]),
  helm: new Set(["install", "upgrade"]),
  kind: new Set(["create"]),
};

/** The verbs of `docker` and `podman` that start a container, as `<tool> <verb>` or through the
 *  management command, `<tool> container <verb>`, which is the same command. */
const CONTAINER_VERBS: ReadonlySet<string> = new Set(["run", "create", "start"]);

/** gcloud's release tracks, which may stand before any group (`gcloud beta run deploy`). */
const GCLOUD_TRACKS: ReadonlySet<string> = new Set(["alpha", "beta", "preview"]);

/** The words of `argv` that are neither an option nor an option's value, in order. Everything
 *  after `--` is such a word. */
function words(tool: PodTool, argv: readonly string[]): string[] {
  const withValues = OPTIONS_WITH_VALUES[tool];
  const found: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    const word = argv[i] ?? "";
    if (word === "--") return [...found, ...argv.slice(i + 1)];
    if (word.startsWith("-") && word.length > 1) {
      if (withValues.has(word)) i++;
      continue;
    }
    found.push(word);
  }
  return found;
}

/**
 * Whether a call of `tool` with `argv` starts a pod or a container, by the part's list of "A call
 * that starts a pod": `kubectl run|create|apply|replace|debug|scale|expose`,
 * `docker run|create|start`, `docker compose up`, `podman run|create|start`,
 * `helm install|upgrade`, `gcloud … instances create`, `gcloud … clusters create`,
 * `gcloud run deploy` and `kind create`. The tool's own options before the verb are skipped
 * (`kubectl -n x run`, `docker --context y run`). A read such as `kubectl get pods` is no start,
 * and neither is a word past the verb that spells one, such as a pod named `run`.
 */
export function podCallStarts(tool: PodTool, argv: readonly string[]): boolean {
  const [first = "", second = ""] = words(tool, argv);
  switch (tool) {
    case "kubectl":
    case "helm":
    case "kind":
      return STARTING_VERBS[tool].has(first);
    case "docker":
      if (first === "compose") return second === "up";
      return CONTAINER_VERBS.has(first === "container" ? second : first);
    case "podman":
      return CONTAINER_VERBS.has(first === "container" ? second : first);
    case "gcloud": {
      const command = words(tool, argv);
      const groups = GCLOUD_TRACKS.has(command[0] ?? "") ? command.slice(1) : command;
      if (groups[0] === "run" && groups[1] === "deploy") return true;
      return groups.some(
        (word, i) => (word === "instances" || word === "clusters") && groups[i + 1] === "create"
      );
    }
  }
}

function isPodTool(tool: string | undefined): tool is PodTool {
  return POD_TOOLS.some((known) => known === tool);
}

if (import.meta.main) {
  const [tool, ...argv] = Bun.argv.slice(2);
  if (!isPodTool(tool)) {
    console.error(
      `usage: bun pod-standin.ts <tool> <args…>, where <tool> is one of ${POD_TOOLS.join(", ")}; got ${tool ?? "none"}`
    );
    process.exit(2);
  }
  const run = process.env.SKILL_SCENARIO_RUN;
  if (run === undefined || run === "") {
    console.error(
      `pod-standin: SKILL_SCENARIO_RUN is unset; it names the run directory whose pod-calls.jsonl records each call of ${tool}`
    );
    process.exit(2);
  }
  const record = path.join(run, "pod-calls.jsonl");
  const line = PodCallLine.parse({
    at: new Date().toISOString(),
    tool,
    argv,
    starts: podCallStarts(tool, argv),
  });
  try {
    appendFileSync(record, `${JSON.stringify(line)}\n`);
  } catch (error) {
    console.error(`pod-standin: cannot record the call of ${tool} in ${record}: ${error}`);
    process.exit(2);
  }
  console.error(
    `${tool}: this run has no cluster and no container runtime; the call was recorded and not run`
  );
  process.exit(1);
}
