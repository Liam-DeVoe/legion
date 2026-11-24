import { z } from "zod";
import { DaemonSetting, type DispatchReads } from "./dispatch-api.ts";

/**
 * Which release defines a name in each place the tool writes: each project's Legion daemon,
 * whose `GET /legion/v1/state` reports `contract.daemon`, and Dispatch's own build, whose
 * `/healthz` reports `commit`. A daemon's code name is rewritten only in a project whose daemon
 * reads at or above the entry's contract, or that the run names as having no Legion daemon; a
 * Dispatch API name only once the running build holds the merge commit the run names.
 */

/** How a project's daemon read, and where its address came from. */
export type DaemonReading =
  | { readonly kind: "contract"; readonly daemon: number; readonly source: Source }
  | { readonly kind: "no-contract-key"; readonly source: Source }
  | { readonly kind: "no-answer"; readonly source: Source }
  /** Dispatch names no daemon for the project and the run gives none. */
  | { readonly kind: "unknown" }
  /** The run names the project as having no Legion daemon (`--no-daemon`). */
  | { readonly kind: "none" };

/** Dispatch's registration, or the address the run gave (`--daemon-state`). */
export type Source = "dispatch" | "run";

export interface DaemonOptions {
  /** Projects the run names as having no Legion daemon. */
  readonly noDaemon: ReadonlySet<string>;
  /** A daemon state address per project, for a project Dispatch names none for. */
  readonly stateAddress: ReadonlyMap<string, string>;
}

/** Reads a daemon's state, given its address (no path). */
export interface DaemonStates {
  read(stateUrl: string): Promise<unknown>;
}

const StateContract = z.looseObject({
  contract: z.looseObject({ daemon: z.int() }).optional(),
});

/** What one state answer says of the daemon's contract. */
export function readingOf(state: unknown, source: Source): DaemonReading {
  const parsed = StateContract.safeParse(state);
  if (!parsed.success) return { kind: "no-answer", source };
  const contract = parsed.data.contract;
  if (contract === undefined) return { kind: "no-contract-key", source };
  return { kind: "contract", daemon: contract.daemon, source };
}

/** Each project's daemon reading. A daemon that does not answer, or answers no JSON object, is
 *  `no-answer`; none of these readings turns a contract on but `contract` at or above it. */
export async function readDaemons(
  projects: readonly string[],
  reads: Pick<DispatchReads, "daemonSetting">,
  states: DaemonStates,
  options: DaemonOptions
): Promise<Map<string, DaemonReading>> {
  const readings = new Map<string, DaemonReading>();
  for (const project of projects) {
    if (options.noDaemon.has(project)) {
      readings.set(project, { kind: "none" });
      continue;
    }
    const setting = DaemonSetting.parse(await reads.daemonSetting(project));
    const given = options.stateAddress.get(project);
    const address = setting?.state_url ?? given;
    if (address === undefined) {
      readings.set(project, { kind: "unknown" });
      continue;
    }
    const source: Source = setting === null ? "run" : "dispatch";
    try {
      readings.set(project, readingOf(await states.read(address), source));
    } catch {
      readings.set(project, { kind: "no-answer", source });
    }
  }
  return readings;
}

/** Whether a daemon's names of `contract` hold in the project: its daemon reads at or above it,
 *  or the run names the project as having no daemon. */
export function daemonAtLeast(reading: DaemonReading | undefined, contract: number): boolean {
  if (reading?.kind === "none") return true;
  return reading?.kind === "contract" && reading.daemon >= contract;
}

/** The words a reading is printed as. */
export function describeReading(reading: DaemonReading): string {
  switch (reading.kind) {
    case "contract":
      return `source=${reading.source} contract=${reading.daemon}`;
    case "no-contract-key":
    case "no-answer":
      return `source=${reading.source} contract=${reading.kind}`;
    case "unknown":
      return "source=none contract=unknown";
    case "none":
      return "source=run contract=no-daemon";
  }
}

/** Dispatch's bound on one state read (`legion/client.go`): 60 seconds and 32 MiB. */
export const STATE_DEADLINE_MS = 60_000;
export const STATE_MAX_BYTES = 32 * 1024 * 1024;

/** Reads a daemon's state as Dispatch's own sampler does: no credential, bounded in time and in
 *  size. A body past the size bound is refused rather than cut. */
export function liveDaemonStates(fetchImpl: typeof fetch): DaemonStates {
  return {
    async read(stateUrl) {
      const response = await fetchImpl(`${stateUrl.replace(/\/+$/, "")}/legion/v1/state`, {
        headers: { Accept: "application/json" },
        signal: AbortSignal.timeout(STATE_DEADLINE_MS),
      });
      if (!response.ok || response.body === null) {
        await response.body?.cancel();
        throw new Error(`state answered ${response.status}`);
      }
      const chunks: Uint8Array[] = [];
      let size = 0;
      for await (const chunk of response.body) {
        size += chunk.length;
        if (size > STATE_MAX_BYTES) throw new Error("state is larger than the bound");
        chunks.push(chunk);
      }
      return JSON.parse(Buffer.concat(chunks).toString("utf8"));
    },
  };
}

/** Whether one commit holds another, asked of a git checkout. */
export interface Ancestry {
  /** True when `merge` is an ancestor of `commit` (or the same commit). */
  holds(merge: string, commit: string): Promise<boolean>;
}

async function git(root: string, ...args: string[]): Promise<number> {
  const child = Bun.spawn(["git", "-C", root, ...args], { stdout: "ignore", stderr: "ignore" });
  return await child.exited;
}

/** `git merge-base --is-ancestor` in the tool's own checkout, fetching `main` first when the
 *  checkout does not know the commit. A commit it still cannot find does not hold the merge. */
export function gitAncestry(root: string): Ancestry {
  return {
    async holds(merge, commit) {
      if ((await git(root, "cat-file", "-e", `${commit}^{commit}`)) !== 0) {
        await git(root, "fetch", "--quiet", "origin", "main");
      }
      return (await git(root, "merge-base", "--is-ancestor", merge, commit)) === 0;
    },
  };
}

const HealthCommit = z.looseObject({
  commit: z
    .string()
    .regex(/^[0-9a-f]{7,40}$/)
    .nullable(),
});

/** The running Dispatch build, and whether it holds the merge commit the run names. A build
 *  that answers no commit holds nothing. */
export async function dispatchBuild(
  health: unknown,
  merge: string,
  ancestry: Ancestry
): Promise<{ readonly commit: string | null; readonly holdsMerge: boolean }> {
  const parsed = HealthCommit.safeParse(health);
  const commit = parsed.success ? parsed.data.commit : null;
  if (commit === null) return { commit, holdsMerge: false };
  return { commit, holdsMerge: await ancestry.holds(merge, commit) };
}
