#!/usr/bin/env bun
// visible-world.ts: every place in one run's world where its agent could read the name of its
// scenario, its case or its label, or the word `ablate`, or the path of a label's checkout or of
// the map. A run measures what its label removes only when its agent cannot learn which label it
// runs under, nor which case it is in.
//
//   bun visible-world.ts <run dir> [--live]
//
// <run dir> is a run's directory, <work>/runs/<id> (rig.sh's layout). The names come from the map,
// <work>/maps: each link <work>/maps/runs/<scenario>[-<case>]-<label>-<n> points at a run's
// directory, and each link <work>/maps/profiles/<label> at a label's profile.
//
// What it reads, the world the run's agent sees:
//   env        the run's pane.env, the agent's whole environment (launch runs it under `env -i`)
//   command    launch.sh and system-args, the agent's command line
//   tree       the path and the bytes of every file under the run's directory (its cwd, HOME,
//              TMPDIR, stand-ins and fixtures), and the target of every symbolic link there
//   transcript the files under <run>/sessions: what the agent read and wrote
//   plugins    the path and the bytes of every file under each directory a link in the run's HOME
//              points at outside the run (the label's packed plugins)
//   git        the commits, their messages and the refs of each git repository under the run
//   listing    the names in each directory above the run's directory, up to /
// and with --live, while the run's agent runs:
//   process    the environment and command line of each process that carries the run's
//              SKILL_SCENARIO_RUN
//   visible    the command line of each process the agent's own /proc shows, the names under
//              <work> as the agent's mount namespace shows them (/proc/<pid>/root), whether its
//              /tmp is its run's own tmp (the machine's is every run's), the names in its /var/tmp
//              and /dev/shm, and whether each label's checkout is empty in its view
//   dispatch   the run's Dispatch database (pg_dump of the database its dispatch.json names), the
//              store its `dispatch` commands read
//
// A hit is a channel, a place, the term's kind (`own` for the run's own scenario, case or label),
// the term and the text around it. A label or case that is a plain word (`head`, `facts`) is
// searched only in the shapes a rig name gives it (`profiles/head`, `skills-head`, `-head-3`), so
// prose that says "head" is no hit. Exit 0: no hit. Exit 1: hits. Exit 2: a usage error. Exit 3:
// a channel enumerated nothing it should have (no file under the run, no agent process with
// --live): the search could not see, and says UNKNOWN rather than clean.
import { execFileSync } from "node:child_process";
import type { Dirent } from "node:fs";
import {
  existsSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  realpathSync,
  statSync,
} from "node:fs";
import path from "node:path";
import { ASKGATE_RUN, OWN_HEAD_RUN } from "./score";

interface Term {
  readonly kind: string;
  readonly own: boolean;
  readonly pattern: RegExp;
  readonly text: string;
}
interface Hit {
  readonly channel: string;
  readonly where: string;
  readonly term: Term;
  readonly context: string;
}
/** One link of <work>/maps/runs, read into its fields. */
interface MappedRun {
  readonly name: string;
  readonly target: string;
  readonly scenario: string;
  readonly case: string;
  readonly label: string;
}
interface RunMap {
  readonly own: MappedRun | undefined;
  readonly runs: MappedRun[];
  readonly labels: string[];
  readonly checkouts: { label: string; checkout: string }[];
  readonly maps: string;
}

const MAX_FILE = 64 * 1024 * 1024;
const escaped = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
/** A name that holds no hyphen, digit or capital is a plain word, searched only in a rig name's shapes. */
const plainWord = (name: string) => /^[a-z]+$/.test(name);

/** The run's own fields and every name the map holds, from the links under <work>/maps. */
function mapOf(work: string, runDir: string): RunMap {
  const runsLinks = path.join(work, "maps", "runs");
  const profileLinks = path.join(work, "maps", "profiles");
  const labels = existsSync(profileLinks) ? readdirSync(profileLinks) : [];
  const runReal = realpathSync(runDir);
  const runs = (existsSync(runsLinks) ? readdirSync(runsLinks) : []).map((name) => {
    const link = path.join(runsLinks, name);
    let target = "";
    try {
      target = realpathSync(link);
    } catch {
      // A link whose run was removed points at nothing.
    }
    const cased = OWN_HEAD_RUN.exec(name) ?? ASKGATE_RUN.exec(name);
    const label =
      labels
        .filter((one) => new RegExp(`-${escaped(one)}-\\d+$`).test(name))
        .sort((a, b) => b.length - a.length)[0] ?? "";
    const prefix = label === "" ? name : name.slice(0, name.lastIndexOf(`-${label}-`));
    const runCase = cased?.[1] ?? "";
    const scenario = runCase === "" ? prefix : prefix.slice(0, -(runCase.length + 1));
    return { name, target, scenario, case: runCase, label };
  });
  const own = runs.find((run) => run.target === runReal);
  const checkouts = labels.flatMap((label) => {
    const file = path.join(profileLinks, label, "checkout");
    return existsSync(file) ? [{ label, checkout: readFileSync(file, "utf8").trim() }] : [];
  });
  return { own, runs, labels, checkouts, maps: path.join(work, "maps") };
}

function termsOf(map: RunMap): Term[] {
  const own = map.own;
  const terms: Term[] = [];
  const add = (kind: string, isOwn: boolean, text: string, pattern: RegExp) => {
    if (!terms.some((term) => term.pattern.source === pattern.source))
      terms.push({ kind, own: isOwn, text, pattern });
  };
  for (const scenario of new Set(map.runs.map((run) => run.scenario))) {
    if (scenario === "") continue;
    const isOwn = scenario === own?.scenario;
    if (plainWord(scenario)) {
      add("scenario", isOwn, `runs/${scenario}-`, new RegExp(`runs/${escaped(scenario)}-`, "g"));
      add("scenario", isOwn, `run_${scenario}_`, new RegExp(`run_${escaped(scenario)}_`, "g"));
    } else add("scenario", isOwn, scenario, new RegExp(escaped(scenario), "g"));
  }
  for (const runCase of new Set(map.runs.map((run) => run.case))) {
    if (runCase === "") continue;
    const isOwn = runCase === own?.case;
    if (plainWord(runCase))
      add("case", isOwn, `-${runCase}-`, new RegExp(`[-_]${escaped(runCase)}[-_]`, "g"));
    else add("case", isOwn, runCase, new RegExp(escaped(runCase), "g"));
  }
  for (const label of map.labels) {
    const isOwn = label === own?.label;
    const squashed = label.replace(/-/g, "");
    add("label", isOwn, `profiles/${label}`, new RegExp(`profiles/${escaped(label)}\\b`, "g"));
    add("label", isOwn, `skills-${label}`, new RegExp(`skills-${escaped(label)}\\b`, "g"));
    add("label", isOwn, `skills${squashed}r`, new RegExp(`skills${escaped(squashed)}r\\d`, "g"));
    add("label", isOwn, `-${label}-<n>`, new RegExp(`[-_]${escaped(label)}[-_]\\d`, "g"));
    if (!plainWord(label)) add("label", isOwn, label, new RegExp(escaped(label), "g"));
  }
  add("word", false, "ablate", /ablate/gi);
  for (const { label, checkout } of map.checkouts)
    add("checkout", label === own?.label, checkout, new RegExp(escaped(checkout), "g"));
  add("map", false, map.maps, new RegExp(escaped(map.maps), "g"));
  add("case-file", false, "case.txt", /(^|\/)case\.txt$/g);
  return terms;
}

const hits: Hit[] = [];
const counts = new Map<string, number>();
const count = (channel: string, n = 1) => counts.set(channel, (counts.get(channel) ?? 0) + n);

function search(channel: string, where: string, text: string, terms: Term[]) {
  for (const term of terms) {
    if (term.kind === "case-file") continue;
    term.pattern.lastIndex = 0;
    for (const match of text.matchAll(term.pattern)) {
      const at = match.index ?? 0;
      const context = text
        .slice(Math.max(0, at - 60), at + match[0].length + 60)
        .replace(/[^\x20-\x7e]/g, " ");
      hits.push({ channel, where, term, context });
      if (hits.filter((hit) => hit.where === where && hit.term === term).length >= 3) break;
    }
  }
}

function searchPath(channel: string, file: string, terms: Term[]) {
  search(channel, `${file} (its path)`, file, terms);
  for (const term of terms)
    if (term.kind === "case-file" && /(^|\/)case\.txt$/.test(file))
      hits.push({ channel, where: file, term, context: "a file named case.txt" });
}

function readText(file: string): string | undefined {
  try {
    if (statSync(file).size > MAX_FILE) {
      count("skipped (over 64 MiB)");
      return undefined;
    }
    return readFileSync(file).toString("latin1");
  } catch {
    count("unreadable");
    return undefined;
  }
}

/** Every file under `dir`, without following a link; each link's target is returned apart. */
function walk(dir: string, files: string[], links: string[]) {
  let entries: Dirent[];
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    count("unreadable");
    return;
  }
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isSymbolicLink()) links.push(full);
    else if (entry.isDirectory()) walk(full, files, links);
    else if (entry.isFile()) files.push(full);
  }
}

function gitRepositories(files: string[]): string[] {
  const repos = new Set<string>();
  for (const file of files) {
    const base = path.basename(file);
    if (base === "HEAD" && existsSync(path.join(path.dirname(file), "objects")))
      repos.add(path.dirname(file));
  }
  return [...repos];
}

function staticWorld(runDir: string, terms: Term[]) {
  const files: string[] = [];
  const links: string[] = [];
  walk(runDir, files, links);
  for (const name of ["pane.env"]) {
    const file = path.join(runDir, name);
    if (existsSync(file)) {
      search("env", file, readFileSync(file, "utf8"), terms);
      count("env", readFileSync(file, "utf8").split("\n").filter(Boolean).length);
    }
  }
  for (const name of ["launch.sh", "system-args", "prompt.txt"]) {
    const file = path.join(runDir, name);
    if (existsSync(file)) {
      search("command", file, readFileSync(file, "utf8"), terms);
      count("command");
    }
  }
  const sessions = path.join(runDir, "sessions");
  for (const file of files) {
    const channel = file.startsWith(`${sessions}/`) ? "transcript" : "tree";
    searchPath(channel, file, terms);
    const text = readText(file);
    if (text !== undefined) search(channel, file, text, terms);
    count(channel);
  }
  const outside = new Set<string>();
  for (const link of links) {
    let target = "";
    try {
      target = readlinkSync(link);
    } catch {
      continue;
    }
    searchPath("tree", link, terms);
    search("tree", `${link} (its target)`, target, terms);
    count("links");
    const resolved = path.resolve(path.dirname(link), target);
    if (!resolved.startsWith(`${runDir}/`) && existsSync(resolved)) outside.add(resolved);
  }
  // The label's plugins and every other place a link of the run points at outside it.
  for (const place of outside) {
    const inner: string[] = [];
    if (statSync(place).isDirectory()) walk(realpathSync(place), inner, []);
    else inner.push(place);
    for (const file of inner) {
      searchPath("plugins", file, terms);
      const text = readText(file);
      if (text !== undefined) search("plugins", file, text, terms);
      count("plugins");
    }
  }
  for (const repo of gitRepositories(files)) {
    try {
      const log = execFileSync(
        "git",
        ["-C", repo, "log", "--all", "--format=%H %an <%ae> %cn <%ce>%n%B"],
        { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }
      );
      const refs = execFileSync("git", ["-C", repo, "for-each-ref", "--format=%(refname)"], {
        encoding: "utf8",
      });
      search("git", `${repo} (log)`, log, terms);
      search("git", `${repo} (refs)`, refs, terms);
      count("git");
    } catch {
      count("unreadable");
    }
  }
  for (let dir = path.dirname(runDir); ; dir = path.dirname(dir)) {
    try {
      for (const name of readdirSync(dir))
        search("listing", dir, `${dir}/${name}`.slice(dir.length), terms);
      count("listing");
    } catch {
      count("unreadable");
    }
    if (dir === "/") break;
  }
  return files.length;
}

function liveWorld(runDir: string, work: string, terms: Term[], map: RunMap): number {
  const marker = `SKILL_SCENARIO_RUN=${runDir}\0`;
  const pids = readdirSync("/proc").filter((pid) => {
    if (!/^\d+$/.test(pid)) return false;
    try {
      return readFileSync(`/proc/${pid}/environ`).toString("latin1").includes(marker);
    } catch {
      return false;
    }
  });
  for (const pid of pids) {
    try {
      search(
        "process",
        `pid ${pid} environ`,
        readFileSync(`/proc/${pid}/environ`).toString("latin1"),
        terms
      );
      search(
        "process",
        `pid ${pid} cmdline`,
        readFileSync(`/proc/${pid}/cmdline`).toString("latin1"),
        terms
      );
      count("process");
    } catch {
      count("unreadable");
    }
  }
  const agent = pids.find((pid) => {
    try {
      return readFileSync(`/proc/${pid}/cmdline`, "latin1").includes("omp");
    } catch {
      return false;
    }
  });
  if (agent !== undefined) {
    const root = `/proc/${agent}/root`;
    try {
      for (const pid of readdirSync(`${root}/proc`).filter((one) => /^\d+$/.test(one))) {
        try {
          search(
            "visible",
            `its /proc/${pid}/cmdline`,
            readFileSync(`${root}/proc/${pid}/cmdline`).toString("latin1"),
            terms
          );
          count("visible processes");
        } catch {
          // A process that ended between the listing and the read.
        }
      }
    } catch {
      console.log(`UNKNOWN visible: cannot read ${root}/proc`);
    }
    const names: string[] = [];
    const list = (dir: string, depth: number) => {
      let entries: string[];
      try {
        entries = readdirSync(`${root}${dir}`);
      } catch {
        return;
      }
      for (const name of entries) {
        const full = path.join(dir, name);
        names.push(full);
        search("visible", `${full} (under its <work>)`, full, terms);
        if (depth > 0 && full !== runDir) {
          try {
            if (statSync(`${root}${full}`).isDirectory()) list(full, depth - 1);
          } catch {
            // Gone, or a socket.
          }
        }
      }
    };
    list(work, 2);
    count("visible work entries", names.length);
    console.log(
      `its <work> shows: ${names.filter((name) => name.split("/").length <= work.split("/").length + 2).join(" ")}`
    );
    if (existsSync(`${root}${work}/maps`))
      hits.push({
        channel: "visible",
        where: `${work}/maps`,
        term: terms.find((term) => term.kind === "map") as Term,
        context: "the agent's mount namespace shows the map",
      });
    // The agent's scratch directories: its /tmp must be its run's own tmp, and /var/tmp and
    // /dev/shm hold nothing, since the machine's are every run's.
    const own = statSync(path.join(runDir, "tmp"));
    const tmp = statSync(`${root}/tmp`);
    if (tmp.dev !== own.dev || tmp.ino !== own.ino)
      hits.push({
        channel: "visible",
        where: "/tmp",
        term: terms.find((term) => term.kind === "case-file") as Term,
        context: "the agent's /tmp is not its run's own tmp: it is the machine's, every run's",
      });
    for (const shared of ["/var/tmp", "/dev/shm"]) {
      const entries = readdirSync(`${root}${shared}`);
      count(`visible ${shared} entries`, entries.length);
      for (const name of entries)
        search("visible", `${shared}/${name}`, `${shared}/${name}`, terms);
    }
    // Every label's checkout is empty in its view, so no agent reads another label's texts.
    for (const { checkout } of map.checkouts) {
      const entries = existsSync(`${root}${checkout}`) ? readdirSync(`${root}${checkout}`) : [];
      count("visible checkouts");
      if (entries.length > 0)
        hits.push({
          channel: "visible",
          where: checkout,
          term: terms.find((term) => term.kind === "checkout") as Term,
          context: `the agent's view shows ${entries.length} entries of a label's checkout`,
        });
    }
  }
  const dispatchFile = path.join(runDir, "dispatch.json");
  if (existsSync(dispatchFile)) {
    const { postgres, database } = JSON.parse(readFileSync(dispatchFile, "utf8")) as {
      postgres?: string;
      database?: string;
    };
    if (postgres && database) {
      try {
        const dump = execFileSync(
          "docker",
          ["exec", postgres, "pg_dump", "-U", "postgres", "--data-only", database],
          { encoding: "latin1", maxBuffer: 512 * 1024 * 1024 }
        );
        search("dispatch", `database ${database}`, dump, terms);
        count("dispatch bytes", dump.length);
      } catch (error) {
        console.log(
          `UNKNOWN dispatch: pg_dump of ${database} failed: ${error instanceof Error ? error.message.split("\n")[0] : error}`
        );
      }
    }
  }
  return pids.length;
}

if (import.meta.main) {
  const args = Bun.argv.slice(2);
  const live = args.includes("--live");
  const runArg = args.find((arg) => !arg.startsWith("--"));
  if (runArg === undefined || !existsSync(runArg)) {
    console.error("usage: visible-world.ts <run dir> [--live]");
    process.exit(2);
  }
  const runDir = realpathSync(runArg);
  const work = path.dirname(path.dirname(runDir));
  const map = mapOf(work, runDir);
  if (map.own === undefined) {
    console.error(`visible-world.ts: no link under ${work}/maps/runs points at ${runDir}`);
    process.exit(2);
  }
  const terms = termsOf(map);
  console.log(
    `run ${map.own.name} (${runDir}): scenario ${map.own.scenario}, case ${map.own.case || "none"}, label ${map.own.label}; ${terms.length} terms from ${map.runs.length} runs and ${map.labels.length} labels`
  );
  const files = staticWorld(runDir, terms);
  const agents = live ? liveWorld(runDir, work, terms, map) : -1;
  for (const hit of hits)
    console.log(
      `${hit.channel}\t${hit.where}\t${hit.term.kind}${hit.term.own ? " (own)" : ""}\t${hit.term.text}\t…${hit.context}…`
    );
  console.log(`read: ${[...counts].map(([channel, n]) => `${channel} ${n}`).join(", ")}`);
  const byChannel = new Map<string, number>();
  for (const hit of hits) byChannel.set(hit.channel, (byChannel.get(hit.channel) ?? 0) + 1);
  console.log(
    `hits: ${hits.length}${hits.length ? ` (${[...byChannel].map(([c, n]) => `${c} ${n}`).join(", ")})` : ""}`
  );
  if (files === 0) {
    console.log(`UNKNOWN: read no file under ${runDir}`);
    process.exit(3);
  }
  if (live && agents === 0) {
    console.log(
      `UNKNOWN: no process carries SKILL_SCENARIO_RUN=${runDir}: run --live while its agent runs`
    );
    process.exit(3);
  }
  process.exit(hits.length === 0 ? 0 : 1);
}
