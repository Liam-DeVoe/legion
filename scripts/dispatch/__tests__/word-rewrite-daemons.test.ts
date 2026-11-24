import { describe, expect, test } from "bun:test";
import {
  type DaemonReading,
  daemonAtLeast,
  dispatchBuild,
  readDaemons,
  readingOf,
} from "../word-rewrite/daemons.ts";
import { fixture } from "./word-rewrite-fake.ts";

const N = 220;

/** Daemon states by address, and the addresses read. */
function states(answers: Record<string, unknown>) {
  const read: string[] = [];
  return {
    read,
    states: {
      async read(url: string) {
        read.push(url);
        if (!Object.hasOwn(answers, url)) throw new Error("no daemon answers");
        return answers[url];
      },
    },
  };
}

function settings(registered: Record<string, unknown>) {
  return {
    daemonSetting: async (project: string) =>
      Object.hasOwn(registered, project) ? registered[project] : fixture("legion-daemon-null.json"),
  };
}

const contractState = (daemon: number) => ({ contract: { daemon, caller: daemon } });

describe("each project's daemon", () => {
  test("contract.daemon at, below and above an entry's contract: only at and above turn it on", async () => {
    const set = fixture("legion-daemon-set.json") as { state_url: string };
    const { states: served } = states({
      [set.state_url]: contractState(N),
      "http://below": contractState(N - 1),
      "http://above": contractState(N + 1),
    });
    const readings = await readDaemons(
      ["AT", "BELOW", "ABOVE"],
      settings({
        AT: set,
        BELOW: { state_url: "http://below" },
        ABOVE: { state_url: "http://above" },
      }),
      served,
      { noDaemon: new Set(), stateAddress: new Map() }
    );
    expect(daemonAtLeast(readings.get("AT"), N)).toBe(true);
    expect(daemonAtLeast(readings.get("BELOW"), N)).toBe(false);
    expect(daemonAtLeast(readings.get("ABOVE"), N)).toBe(true);
    expect(readings.get("AT")).toEqual({ kind: "contract", daemon: N, source: "dispatch" });
  });

  test("a real daemon's state answer: daemon-state-contract.json", () => {
    expect(readingOf(fixture("daemon-state-contract.json"), "dispatch")).toEqual({
      kind: "contract",
      daemon: 188,
      source: "dispatch",
    });
  });

  test("a state with no contract key is older than every entry", async () => {
    const { states: served } = states({ "http://old": fixture("daemon-state-no-contract.json") });
    const readings = await readDaemons(
      ["OLD"],
      settings({ OLD: { state_url: "http://old" } }),
      served,
      {
        noDaemon: new Set(),
        stateAddress: new Map(),
      }
    );
    expect(readings.get("OLD")).toEqual({ kind: "no-contract-key", source: "dispatch" });
    expect(daemonAtLeast(readings.get("OLD"), 1)).toBe(false);
  });

  test("no daemon set and none given keeps every name, and reads no state", async () => {
    const { read, states: served } = states({});
    const readings = await readDaemons(["NOADDR"], settings({}), served, {
      noDaemon: new Set(),
      stateAddress: new Map(),
    });
    expect(readings.get("NOADDR")).toEqual({ kind: "unknown" });
    expect(daemonAtLeast(readings.get("NOADDR"), 1)).toBe(false);
    expect(read).toEqual([]);
  });

  test("a daemon that does not answer keeps every name", async () => {
    const { states: served } = states({});
    const readings = await readDaemons(
      ["GONE"],
      settings({ GONE: { state_url: "http://gone" } }),
      served,
      {
        noDaemon: new Set(),
        stateAddress: new Map(),
      }
    );
    expect(readings.get("GONE")).toEqual({ kind: "no-answer", source: "dispatch" });
    expect(daemonAtLeast(readings.get("GONE"), 1)).toBe(false);
  });

  test("an address the run gives for a project Dispatch names no daemon for", async () => {
    const { read, states: served } = states({ "http://given": contractState(N) });
    const readings = await readDaemons(["GIVEN"], settings({}), served, {
      noDaemon: new Set(),
      stateAddress: new Map([["GIVEN", "http://given"]]),
    });
    expect(readings.get("GIVEN")).toEqual({ kind: "contract", daemon: N, source: "run" });
    expect(read).toEqual(["http://given"]);
  });

  test("a project the run names as having no Legion daemon turns every contract on", async () => {
    const { read, states: served } = states({});
    const readings = await readDaemons(["NONE"], settings({}), served, {
      noDaemon: new Set(["NONE"]),
      stateAddress: new Map(),
    });
    const none: DaemonReading | undefined = readings.get("NONE");
    expect(none).toEqual({ kind: "none" });
    expect(daemonAtLeast(none, 10_000)).toBe(true);
    expect(read).toEqual([]);
  });
});

describe("Dispatch's own build", () => {
  test("holds the merge only where git says the merge is an ancestor of its commit", async () => {
    // dispatch-healthz.json: production Dispatch's build, which names its commit.
    const health = fixture("dispatch-healthz.json") as { commit: string };
    expect(health.commit).toMatch(/^[0-9a-f]{40}$/);
    const asked: string[] = [];
    const yes = await dispatchBuild(health, "abc1234", {
      holds: async (merge, commit) => {
        asked.push(`${merge} ${commit}`);
        return true;
      },
    });
    expect(yes).toEqual({ commit: health.commit, holdsMerge: true });
    expect(asked).toEqual([`abc1234 ${health.commit}`]);
    const no = await dispatchBuild(health, "abc1234", { holds: async () => false });
    expect(no.holdsMerge).toBe(false);
  });

  test("a build that names no commit holds nothing: dispatch-healthz-local.json", async () => {
    // The local Dispatch, built from a checkout with no commit stamped in, answers `commit: null`.
    const build = await dispatchBuild(fixture("dispatch-healthz-local.json"), "abc1234", {
      holds: async () => true,
    });
    expect(build).toEqual({ commit: null, holdsMerge: false });
  });
});
