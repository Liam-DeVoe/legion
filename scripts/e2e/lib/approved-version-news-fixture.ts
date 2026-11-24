import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { type StopSignal, stopHere } from "./run-verdict-fixture";

// What approved-version-news-verdicts.test.ts and approved-version-news-runs.test.ts both read
// and write of approved-version-news.sh: the script, its steps, the plan the benchmark prints of
// them, its run lines, the measurements that retired a base arm, and the stand-ins of the
// commands the script's run reaches.

export const script = join(import.meta.dir, "..", "approved-version-news.sh");

export const bench = "BenchmarkRealSessionsTellAndAnswerANewVersion";
/** The benchmark's body, the one test it runs through testing.RunTests. */
export const body = "RealSessionsTellAndAnswerANewVersion";

export const a4 = "a4-the-architect-tells-the-child-reviewer";
export const off = "off-the-architect-tells-the-holders";
export const answers = "answers-a-stale-reviewer-moves-back-a-current-one-comments";

/** A plan as the driver prints it (sessionsPick.plan): a `plan step` line for each step it
 * makes, its word, its name and the arms it makes the step in, then `plan runs`. */
export const plan = (steps: [word: string, step: string, arms: string][], runs: number) =>
  [
    ...steps.map(([word, step, arms]) => `plan step ${word} ${step} ${arms}`),
    `plan runs ${runs}`,
  ].join("\n");
// The driver's plan of a full run at this change: a4 and answers in the head arm alone, their base
// arms retired, and off in both, three runs of each arm.
export const fullPlan = plan(
  [
    ["a4", a4, "head"],
    ["off", off, "head base"],
    ["answers", answers, "head"],
  ],
  3
);
// One run's line as the driver prints it at the run's end.
export const runLine = (step: string, arm: string, n: number, outcome: string) =>
  `run ${step} ${arm} ${n}: ${outcome} in 140s, 31 model calls`;
/** Each run line of STEP in ARM, numbered from 1, one for each outcome. */
export const runsOf = (step: string, arm: string, ...outcomes: string[]) =>
  outcomes.map((outcome, i) => runLine(step, arm, i + 1, outcome));
export const passed = "PASS";
export const failed =
  "FAIL: the reviewer of AS1-2 was sent no Envoy message naming version 2 and the change";
// Each step whose base arm is retired, its word, and what the last line says in its place, the
// measurement that retired it.
export const retired = [
  {
    step: a4,
    word: "a4",
    headOnly: "(head only: the base arm passed 10 of 10 at 578c6abb, main meets R4 since #4203)",
  },
  {
    step: answers,
    word: "answers",
    headOnly:
      "(head only: the base arm passed 10 of 10 at 578c6abb, the daemon's news task states the rule in both arms)",
  },
];

// What `go test` prints around the benchmark's own lines, captured on 2026-11-24 (go1.27.3) from
// packages/daemon, each with the command written above it:
//   env -u LEGION_TEST_PG_DSN go test -count=1 -run '^$' \
//     -bench '^BenchmarkRealSessionsTellAndAnswerANewVersion$' -benchtime=1x ./internal/daemon -args -part-two-plan
// prints the plan, then planTrailer; with APPROVED_VERSION_NEWS_STEP=a5 it prints the refusal line,
// then refusedTrailer, and exits 1. The same command with -v and without -args -part-two-plan, and
// LEGION_TEST_OMP and LEGION_TEST_PG_DSN unset, prints verboseHeader, the plan, skippedEnd; a run
// that passes ends in passedEnd, as the plan's -v run did, and one whose body failed in failedEnd,
// as the refusal did.
const pkg = "github.com/sjawhar/legion/daemon/internal/daemon";
const header = ["goos: linux", "goarch: amd64", `pkg: ${pkg}`, "cpu: AMD EPYC 7B13"];
const benchLine = `${bench}-2   \t       1\t    372550 ns/op`;
const planTrailer = [...header, benchLine, "PASS", `ok  \t${pkg}\t0.058s`];
const refusedTrailer = (refusal: string) => [
  `--- FAIL: ${body} (0.00s)`,
  `    approved_version_news_sessions_live_test.go:293: ${refusal}`,
  `--- FAIL: ${bench}`,
  "    approved_version_news_sessions_live_test.go:278: part two failed; its plan, its runs and the line of each are above",
  "FAIL",
  "exit status 1",
  `FAIL\t${pkg}\t0.109s`,
  "FAIL",
];
const verboseHeader = [...header, bench, `=== RUN   ${body}`];
const passedEnd = [`--- PASS: ${body} (0.00s)`, benchLine, "PASS", `ok  \t${pkg}\t0.047s`];
const skippedEnd = [
  "    approved_version_news_sessions_live_test.go:304: LEGION_TEST_OMP names no pinned Oh My Pi binary, and every pane of this proof runs one",
  `--- SKIP: ${body} (0.00s)`,
  benchLine,
  "PASS",
  `ok  \t${pkg}\t0.059s`,
];
const failedEnd = [`--- FAIL: ${body} (0.00s)`, ...refusedTrailer("").slice(2)];

/** How the benchmark's body ended in a stand-in run. */
type Ended = "PASS" | "FAIL" | "SKIP";

/** Where a stand-in sends its signal: the plan check's run of the benchmark, or the sessions'. */
type KillPoint = "plan" | "sessions";

/** What one stand-in run of the script asks for. */
export type NewsRun = {
  /** The plan the benchmark prints of a full run, and of the pick where the run picks. A plan
   * that starts `plan refused: ` is the driver's refusal of the pick. */
  plans: { full: string; pick?: string };
  /** The driver's own lines in the sessions' run, after its plan: each run's line and the rest. */
  sessions: string[];
  /** How the benchmark's body ended. */
  ended?: Ended;
  /** Where a stand-in signals the run's whole process group; unset, the run goes to its end. */
  kill?: { at: KillPoint; signal: StopSignal };
};

/** The model key the run's key file holds: its first 8 characters are in nothing the run keeps. */
export const modelKey = "Kq81mvXz-a-model-key-of-the-fixture";

/**
 * Lays out in DIR the commands approved-version-news.sh's run reaches, and returns the environment
 * the script runs in, on a host with no model, no Oh My Pi, no libpq and no time for a Go build:
 *
 *   - `go` answers the plan check's `go … test … -args -part-two-plan` with the plan RUN.plans
 *     holds, as the benchmark prints it, and the sessions' `go … test … -bench …` with the plan of
 *     the run's pick, RUN.sessions and the end RUN.ended; it writes the arguments and the inputs
 *     each call got, one line a call, to DIR/go-calls;
 *   - `python3` answers lib/loopback-dsn.py with 127.0.0.1, the host a loopback DSN names;
 *   - `jj` answers `jj --version` with the oldest release the daemon's boot starts with;
 *   - LEGION_TEST_OMP names a stand-in that does nothing, and the key file holds modelKey.
 *
 * With `run.kill`, the stand-in at that point signals its whole process group before it prints
 * anything, so the script must run in a process group of its own (inOwnGroup).
 */
export function newsStandIns(dir: string, run: NewsRun): Record<string, string> {
  const bin = join(dir, "bin");
  mkdirSync(bin, { recursive: true });
  mkdirSync(join(dir, "tmp"), { recursive: true });
  const command = (path: string, body: string) => {
    writeFileSync(path, `#!/usr/bin/env bash\n${stopHere}\n${body}\n`);
    chmodSync(path, 0o755);
  };
  const lines = (name: string, text: string[]) =>
    writeFileSync(join(dir, name), text.map((line) => `${line}\n`).join(""));
  const planOutput = (text: string) =>
    text.startsWith("plan refused: ")
      ? [text, ...refusedTrailer(text.slice("plan refused: ".length))]
      : [...text.split("\n"), ...planTrailer];
  const pickPlan = run.plans.pick ?? run.plans.full;
  lines("full-plan-output", planOutput(run.plans.full));
  lines("pick-plan-output", planOutput(pickPlan));
  const ended = run.ended ?? "PASS";
  const end = { PASS: passedEnd, FAIL: failedEnd, SKIP: skippedEnd }[ended];
  lines("sessions-output", [...verboseHeader, ...pickPlan.split("\n"), ...run.sessions, ...end]);
  command(
    join(bin, "go"),
    `calls=$STAND_IN_DIR/go-calls
case " $* " in
  *" -part-two-plan "*)
    echo "plan: $* | dsn=\${LEGION_TEST_PG_DSN:-unset}" >>"$calls"
    stop_here plan
    output=full-plan-output
    [ -z "\${APPROVED_VERSION_NEWS_STEP:-}\${APPROVED_VERSION_NEWS_ARM:-}\${APPROVED_VERSION_NEWS_RUNS:-}" ] ||
      output=pick-plan-output
    cat "$STAND_IN_DIR/$output"
    ! grep -q '^plan refused: ' "$STAND_IN_DIR/$output"
    ;;
  *" -bench "*)
    prompt=$APPROVED_VERSION_NEWS_BASE_PROMPTS/packages/daemon/internal/prompts/roles/reviewer.md
    echo "sessions: $* | prompt=$(cat "$prompt") | commit=$APPROVED_VERSION_NEWS_BASE_COMMIT | logs=$LEGION_TEST_DAEMON_LOG_DIR | key=$APPROVED_VERSION_NEWS_MODEL_KEY_FILE" >>"$calls"
    stop_here sessions
    cat "$STAND_IN_DIR/sessions-output"
    [ ${ended} != FAIL ]
    ;;
  *)
    echo "the stand-in go answers the benchmark's plan and its run alone, not: $*" >&2
    exit 1
    ;;
esac`
  );
  command(join(bin, "python3"), "echo 127.0.0.1");
  command(
    join(bin, "jj"),
    `[ "$1" = --version ] || { echo "the fixture's jj answers --version alone" >&2; exit 1; }
echo "jj 0.38.0"`
  );
  command(join(dir, "omp"), "exit 0");
  writeFileSync(join(dir, "model-key"), `${modelKey}\n`);
  const env: Record<string, string> = { ...(process.env as Record<string, string>) };
  for (const name of [
    "APPROVED_VERSION_NEWS_STEP",
    "APPROVED_VERSION_NEWS_ARM",
    "APPROVED_VERSION_NEWS_RUNS",
    "ANTHROPIC_API_KEY",
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_INDEX_FILE",
    "LEGION_JJ_PATH",
  ]) {
    delete env[name];
  }
  return {
    ...env,
    PATH: `${bin}:${process.env.PATH ?? ""}:${dirname(process.execPath)}`,
    TMPDIR: join(dir, "tmp"),
    LEGION_TEST_PG_DSN: "postgres://fixture@127.0.0.1:1/fixture?sslmode=disable",
    LEGION_TEST_OMP: join(dir, "omp"),
    ANTHROPIC_BASE_URL: "http://127.0.0.1:1",
    APPROVED_VERSION_NEWS_MODEL_KEY_FILE: join(dir, "model-key"),
    APPROVED_VERSION_NEWS_EVIDENCE_DIR: join(dir, "evidence"),
    STAND_IN_DIR: dir,
    STAND_IN_KILL_AT: run.kill?.at ?? "",
    STAND_IN_KILL_SIGNAL: run.kill?.signal ?? "",
  };
}
