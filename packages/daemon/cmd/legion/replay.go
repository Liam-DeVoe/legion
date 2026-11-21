package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/replay"
)

// newReplayCommand is `legion replay`, a developer's tool rather than an operator's: it replays a
// recorded window of a daemon's project through the daemon's own admission pass under a named
// slot rule (internal/replay), so a rule is judged by the daemon's own decisions over a window it
// lived through. Its commands collect a window (collect, collect-merges), neutralize one for the
// repository's test data (neutralize), replay one (run), read the measures of a window's record
// (scorecard) and name the rules a replay can run (rules). None of them reaches a daemon.
func newReplayCommand(stdout, stderr io.Writer) *cobra.Command {
	return group(&cobra.Command{Use: "replay", Short: "replay a recorded window of a daemon's project through the daemon's own admission pass under a named slot rule (a developer's tool)"},
		newReplayCollectCommand(stdout, stderr),
		newReplayCollectMergesCommand(stdout, stderr),
		newReplayNeutralizeCommand(stdout, stderr),
		newReplayRunCommand(stdout, stderr),
		newReplayScorecardCommand(stdout, stderr),
		newReplayRulesCommand(stdout, stderr),
	)
}

// replayCollectLong is `legion replay collect --help`'s description.
const replayCollectLong = `Collect a recorded window of a daemon's project into a directory of its own.

The daemon's lines are read from the files and directories --log names, which a person
collected: the daemon's own standard error, ` + "`kubectl logs`" + ` output (with or without
--timestamps and --prefix), or a Cloud Logging export of {timestamp, jsonPayload} rows as JSON
lines or one JSON array; any of them gzipped or not. The lines of every kind the replay's trace
reads, written in [--from − --before, --to), are kept in time order, a line read twice once.

The events of every issue those lines name, and of every blocker, parent and child those
issues' events name, in any project, are read from Dispatch (DISPATCH_URL) from each issue's
first event up to --to, with GET alone. The token is the file --dispatch-token-file names, else
the file DISPATCH_TOKEN_FILE names, else DISPATCH_TOKEN; it is never printed.

window.json records the project, the span, --before, the cap of each change the slot lines and
the daemon's start lines show, the line kinds the log holds, the deployment's settings (the flags
below, each defaulting to the daemon's own default), the parts of the span (--part), the
collector's commit (--collector, else the binary's own) and the sha256 of every file. Run
collect-merges next for each base branch.`

// newReplayCollectCommand is `legion replay collect` (replay.Collect).
func newReplayCollectCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collect --project <KEY> --from <time> --to <time> --before <duration> --log <file|dir>... --out <dir>",
		Short: "collect a recorded window: the daemon's lines from log files, and the Dispatch events of the issues they name",
		Long:  replayCollectLong,
		Args:  noArguments,
	}
	defaults, defaultsErr := replay.DaemonSettings()
	flags := cmd.Flags()
	project := flags.String("project", "", "the Dispatch project key of the daemon the lines are from (required)")
	from := flags.String("from", "", "the window's start, RFC 3339 (required)")
	to := flags.String("to", "", "the window's end, RFC 3339 (required)")
	before := flags.Duration("before", 0, "how far before --from the inputs reach, so the state at --from can be rebuilt (required)")
	logs := flags.StringArray("log", nil, "a file or a directory of files of the daemon's lines; repeat for more (required)")
	out := flags.String("out", "", "the directory the window is written to, missing or empty (required)")
	tokenFile := flags.String("dispatch-token-file", "", "the file holding the Dispatch token (else DISPATCH_TOKEN_FILE, else DISPATCH_TOKEN)")
	concurrency := flags.Int("concurrency", replay.DefaultConcurrency, "how many issues are read from Dispatch at once")
	collector := flags.String("collector", "", "the commit of cybermonklabs/legion this collect runs from (else the binary's own)")
	parts := flags.StringArray("part", nil, "a part of the span as <from>,<to>,<release>,<commit>,<rule>; repeat for each part")
	tick := flags.Duration("tick-interval", defaults.TickInterval, "the deployment's controller_wake_interval_seconds, the period of the tick passes")
	gateSteps := flags.String("gate-steps", designSteps(defaults.GateSteps), "the deployment's gates.design steps, comma-separated, or off")
	linger := flags.Duration("linger", defaults.Linger, "the deployment's linger_hours")
	rounds := flags.Int("review-round-cap", defaults.ReviewRoundCap, "the deployment's review_round_cap")
	stalled := flags.Duration("stalled-tree-notice", defaults.StalledTreeNotice, "the deployment's stalled_tree_notice_hours")
	silence := flags.Duration("worker-silence", defaults.WorkerSilence, "the deployment's worker_silence_minutes")
	background := flags.Duration("worker-background", defaults.WorkerBackground, "the deployment's worker_background_hours")
	cmd.RunE = runs(func(ctx context.Context, _ []string) int {
		if defaultsErr != nil {
			fmt.Fprintf(stderr, "legion replay collect: %v\n", defaultsErr)
			return 1
		}
		if missing := missingFlag(cmd, "project", "from", "to", "before", "log", "out"); missing != "" {
			return refuse(cmd, "--%s is required", missing)
		}
		span, code := replaySpan(cmd, *from, *to)
		if code != 0 {
			return code
		}
		steps, err := parseDesignSteps(*gateSteps)
		if err != nil {
			return refuse(cmd, "--gate-steps: %v", err)
		}
		var windowParts []replay.Part
		for _, text := range *parts {
			part, err := parsePart(text)
			if err != nil {
				return refuse(cmd, "--part %q: %v", text, err)
			}
			windowParts = append(windowParts, part)
		}
		commit := *collector
		if commit == "" {
			if commit = binaryCommit(); commit == "" {
				return refuse(cmd, "this binary carries no commit of its own; name the commit it was built from with --collector")
			}
		}
		base := os.Getenv("DISPATCH_URL")
		if base == "" {
			fmt.Fprintln(stderr, "legion replay collect: DISPATCH_URL is not set; set it to the Dispatch server the events are read from")
			return 1
		}
		token, err := dispatchToken(*tokenFile)
		if err != nil {
			fmt.Fprintf(stderr, "legion replay collect: %v\n", err)
			return 1
		}
		collected, err := replay.Collect(ctx, replay.CollectConfig{
			Project: *project, From: span[0], To: span[1], Before: *before, Logs: *logs,
			Dispatch: dispatch.New(base, token), Concurrency: *concurrency, Parts: windowParts, Collector: commit, Out: *out,
			Settings: replay.Settings{TickInterval: *tick, GateSteps: steps, Linger: *linger, ReviewRoundCap: *rounds,
				StalledTreeNotice: *stalled, WorkerSilence: *silence, WorkerBackground: *background},
		})
		if err != nil {
			fmt.Fprintf(stderr, "legion replay collect: %v\n", err)
			return 1
		}
		w := collected.Window
		fmt.Fprintf(stdout, "legion replay collect: wrote %s: %d of %d daemon lines kept (%d kinds, %d read twice), %d lines that are no daemon line, the events of %d issues\n",
			*out, collected.Kept, collected.Read, len(w.Lines), collected.Duplicates, collected.NotLines, len(w.Events))
		if len(collected.Missing) > 0 {
			fmt.Fprintf(stdout, "legion replay collect: Dispatch has no issue %s\n", strings.Join(collected.Missing, ", "))
		}
		return 0
	})
	return cmd
}

// newReplayCollectMergesCommand is `legion replay collect-merges` (replay.CollectMerges).
func newReplayCollectMergesCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collect-merges --repo <owner/name> --branch <name> --from <time> --to <time> --github-token-env <VAR> --out <dir>",
		Short: "add a base branch's first-parent history in a span, read from GitHub, to a collected window",
		Long: `Read a base branch's first-parent history in [--from, --to) from GitHub's commit listing, with
GET alone, and write it into the window collect wrote in --out as merges/<owner>__<name>.jsonl,
beside the commits of the repository's other base branches, with window.json's digest of the
file written again. The token is read from the environment variable --github-token-env names,
and is never printed. Run it once for each base branch the window's pull requests name.`,
		Args: noArguments,
	}
	flags := cmd.Flags()
	repo := flags.String("repo", "", "the repository as owner/name (required)")
	branch := flags.String("branch", "", "the base branch (required)")
	from := flags.String("from", "", "the span's start, RFC 3339 (required)")
	to := flags.String("to", "", "the span's end, RFC 3339 (required)")
	tokenEnv := flags.String("github-token-env", "", "the environment variable that holds the GitHub token (required)")
	api := flags.String("github-api", replay.DefaultGitHubAPI, "GitHub's REST API")
	out := flags.String("out", "", "the directory of the window collect wrote (required)")
	cmd.RunE = runs(func(ctx context.Context, _ []string) int {
		if missing := missingFlag(cmd, "repo", "branch", "from", "to", "github-token-env", "out"); missing != "" {
			return refuse(cmd, "--%s is required", missing)
		}
		span, code := replaySpan(cmd, *from, *to)
		if code != 0 {
			return code
		}
		token := strings.TrimSpace(os.Getenv(*tokenEnv))
		if token == "" {
			fmt.Fprintf(stderr, "legion replay collect-merges: the environment variable %s holds no token\n", *tokenEnv)
			return 1
		}
		merges, err := replay.CollectMerges(ctx, replay.MergesConfig{Repo: *repo, Branch: *branch, From: span[0], To: span[1],
			Token: token, API: *api, Window: *out})
		if err != nil {
			fmt.Fprintf(stderr, "legion replay collect-merges: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "legion replay collect-merges: wrote %d first-parent commits of %s %s into %s\n", len(merges), *repo, *branch, *out)
		return 0
	})
	return cmd
}

// newReplayNeutralizeCommand is `legion replay neutralize` (replay.Neutralize).
func newReplayNeutralizeCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "neutralize --in <dir> --out <dir> --project <KEY> --map <file>",
		Short: "write a collected window with every private name renamed, for test data a public repository can hold",
		Long: `Write the window in --in to --out with every private name renamed, the same name to the same
neutral name in every file: each issue key of every project and each claim token that spells
one, each repository but cybermonklabs/legion, each actor's and session's id, each session's
title, each person's login and email, each host name and machine name, each IPv4 and IPv6
address but a loopback, unspecified or documentation one, which becomes an address of the
documentation ranges (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24, 2001:db8::/32), each uuid,
link, label and digest, and the free text of Dispatch's events. Every member, JSON type and list
length is kept, and so are the daemon's msgs, ranks, statuses and times. The window's own project
becomes --project, every other OTHERA, OTHERB, …. A window holding more IPv4 addresses than those
ranges hold is refused.

The map of every name replaced is written to --map, which must be outside --out and not there
yet: it holds every private name, so it is never committed. --deny names a word no file may
hold in any case (an organization's name): it is replaced wherever it stands, and one left
anywhere fails the run.`,
		Args: noArguments,
	}
	flags := cmd.Flags()
	in := flags.String("in", "", "the window's directory (required)")
	out := flags.String("out", "", "the directory the neutral window is written to, missing or empty (required)")
	project := flags.String("project", "ACME", "the project key the window's own project becomes")
	mapFile := flags.String("map", "", "the file the map of names is written to, outside --out (required)")
	deny := flags.StringArray("deny", nil, "a word no file of the neutral window may hold, in any case; repeat for more")
	cmd.RunE = runs(func(_ context.Context, _ []string) int {
		if missing := missingFlag(cmd, "in", "out", "map"); missing != "" {
			return refuse(cmd, "--%s is required", missing)
		}
		names, err := replay.Neutralize(replay.NeutralizeConfig{In: *in, Out: *out, Project: *project, Map: *mapFile, Deny: *deny})
		if err != nil {
			fmt.Fprintf(stderr, "legion replay neutralize: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "legion replay neutralize: wrote %s, with %d issue keys and %d people renamed; the map is %s\n",
			*out, len(names.Keys), len(names.People), *mapFile)
		return 0
	})
	return cmd
}

// replayRunLong is `legion replay run --help`'s description.
const replayRunLong = `Replay the recorded window in --window through the daemon's own handler chain (the Dispatch
mirror, the workflow engine and the admission pass) under the slot rule --rule names, on a
simulated clock, in a database of the replay's own that it makes on the Postgres server
--pg-dsn names (from template1, with the daemon's migrations) and drops at the end unless
--keep-db. It writes nothing to the database --pg-dsn names itself, and refuses a server it
cannot make a database on, with the server's reason.

The window's Dispatch events, its recorded phase moves, ticks and boots are applied at their
recorded times or offsets, a worker stub runs the outbox rows the daemon's lanes would take after
each fact, and each work segment runs its recorded length times f^((n − m)/42) at --slowdown f.
Nothing outside is asked: no Dispatch, no GitHub, no NATS.

--out receives log.jsonl (the daemon's lines on the simulated clock), scorecard.json (the
measures read from the states the daemon's own admission passes left), scorecard-line.json (the
same measures read back from log.jsonl and the replay's Dispatch events, as a record's are),
run.json (what ran, what it counted and the gap of each measure between the two scorecards) and,
with --trace-segments, segments.jsonl. The output names each assumption the run rests on and the
scenario it gave the work of an issue that never started.`

// newReplayRunCommand is `legion replay run` (replay.Run).
func newReplayRunCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run --window <dir> --rule <name> --pg-dsn <dsn> --out <dir> [--branch <name>] [--seed <n>] [--slowdown <f>] [--keep-db] [--trace-segments]",
		Short: "replay a recorded window under a slot rule, in a database of its own, and write what it ran and counted",
		Long:  replayRunLong,
		Args:  noArguments,
	}
	flags := cmd.Flags()
	windowDir := flags.String("window", "", "the directory of the recorded window (required)")
	ruleName := flags.String("rule", "", "the slot rule to replay under, as legion replay rules names it (required)")
	branchName := flags.String("branch", replay.MainBranch.Name, "the branch: main, or main+<knob>=<value>")
	seed := flags.Int64("seed", 1, "the seed of every draw the replay makes")
	slowdown := flags.Float64("slowdown", 1, "the factor f a segment's recorded length is scaled by for each 42 issues in flight above the record's")
	dsn := flags.String("pg-dsn", "", "a Postgres server the replay makes its own database on (required)")
	out := flags.String("out", "", "the directory the run's output is written to (required)")
	keepDB := flags.Bool("keep-db", false, "keep the replay's database after the run")
	traceSegments := flags.Bool("trace-segments", false, "write each work segment's n, m and length to segments.jsonl")
	cmd.RunE = runs(func(ctx context.Context, _ []string) int {
		if missing := missingFlag(cmd, "window", "rule", "pg-dsn", "out"); missing != "" {
			return refuse(cmd, "--%s is required", missing)
		}
		rule, ok := record.SlotRuleNamed(*ruleName)
		if !ok {
			return refuse(cmd, "--rule %q names no slot rule; `legion replay rules` lists them", *ruleName)
		}
		branch, err := replay.BranchNamed(*branchName)
		if err != nil {
			return refuse(cmd, "--branch: %v", err)
		}
		if *slowdown <= 0 {
			return refuse(cmd, "--slowdown %v: the factor must be above zero", *slowdown)
		}
		window, err := replay.ReadWindow(*windowDir)
		if err != nil {
			fmt.Fprintf(stderr, "legion replay run: %v\n", err)
			return 1
		}
		result, err := replay.Run(ctx, replay.RunConfig{Window: window, Rule: rule, Branch: branch, Seed: *seed, Slowdown: *slowdown,
			PGDSN: *dsn, Out: *out, KeepDB: *keepDB, TraceSegments: *traceSegments})
		if err != nil {
			fmt.Fprintf(stderr, "legion replay run: %v\n", err)
			return 1
		}
		kept := "dropped"
		if *keepDB {
			kept = "kept"
		}
		counted := result.Divergence
		fmt.Fprintf(stdout, "legion replay run: replayed %s under the rule %s on the branch %s (seed %d, f %v) in the database %s (%s); wrote %s\n",
			*windowDir, rule.Name, branch.Name, *seed, *slowdown, result.Database, kept, *out)
		fmt.Fprintf(stdout, "legion replay run: %d moves refused, %d reassertion starts, %d events fenced, %d starts with no segment left, %d segments passed over, %d unplaced issues\n",
			len(counted.Refused), counted.Reassertions, counted.Fenced, counted.NoSegment, counted.SkippedSegments, len(result.Unplaced))
		for _, refused := range counted.Refused {
			fmt.Fprintf(stdout, "legion replay run: refused %s %s→%s by %s at %s: %s\n", refused.Issue, refused.From, refused.To, refused.Trigger,
				refused.At.Format(time.RFC3339), refused.Reason)
		}
		fmt.Fprintf(stdout, "legion replay run: the scenario of the work of an issue that never started: %s\n", result.Scorecard.Scenario)
		if widest, ok := widestGap(result.Gaps); ok && widest.Gap() != 0 {
			fmt.Fprintf(stdout, "legion replay run: the widest gap between the snapshot and the line scorecards is %s: %v and %v\n",
				widest.Measure, widest.Snapshot, widest.Line)
		} else {
			fmt.Fprintln(stdout, "legion replay run: the snapshot and the line scorecards agree on every measure")
		}
		fmt.Fprintln(stdout, "legion replay run: the assumptions it rests on:")
		for _, number := range result.Assumptions {
			for _, assumption := range replay.Assumptions {
				if assumption.Number != number {
					continue
				}
				if finished := counted.Finished[number]; finished > 0 {
					fmt.Fprintf(stdout, "  %d. %s (%d outbox rows finished under it)\n", number, assumption.Text, finished)
				} else {
					fmt.Fprintf(stdout, "  %d. %s\n", number, assumption.Text)
				}
			}
		}
		return 0
	})
	return cmd
}

// widestGap is the measure of gaps whose snapshot and line values stand furthest apart, and false
// where gaps holds none.
func widestGap(gaps []replay.MeasureGap) (replay.MeasureGap, bool) {
	var widest replay.MeasureGap
	for i, gap := range gaps {
		if i == 0 || math.Abs(gap.Gap()) > math.Abs(widest.Gap()) {
			widest = gap
		}
	}
	return widest, len(gaps) > 0
}

// newReplayScorecardCommand is `legion replay scorecard` (replay.Score): the measures of a recorded
// window read from its record alone, by the code a replay's are read by, with no replay and no
// database.
func newReplayScorecardCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scorecard --window <dir> [--json]",
		Short: "read every measure of a recorded window from its record alone, with no replay",
		Args:  noArguments,
	}
	flags := cmd.Flags()
	windowDir := flags.String("window", "", "the directory of the recorded window (required)")
	asJSON := flags.Bool("json", false, "print the scorecard as JSON")
	cmd.RunE = runs(func(context.Context, []string) int {
		if missing := missingFlag(cmd, "window"); missing != "" {
			return refuse(cmd, "--%s is required", missing)
		}
		window, err := replay.ReadWindow(*windowDir)
		if err != nil {
			fmt.Fprintf(stderr, "legion replay scorecard: %v\n", err)
			return 1
		}
		card, err := replay.Score(window)
		if err != nil {
			fmt.Fprintf(stderr, "legion replay scorecard: %v\n", err)
			return 1
		}
		if *asJSON {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(card); err != nil {
				return 1
			}
			return 0
		}
		fmt.Fprintf(stdout, "the record of %s from %s to %s\n", window.Project, card.From.Format(time.RFC3339), card.To.Format(time.RFC3339))
		for _, measured := range replay.MeasuresOf(card) {
			fmt.Fprintf(stdout, "%s %s\n", measured.Name, strconv.FormatFloat(measured.Value, 'f', -1, 64))
		}
		for _, key := range slices.Sorted(maps.Keys(card.FirstSlot)) {
			fmt.Fprintf(stdout, "firstSlot[%s] %s\n", key, card.FirstSlot[key].Format(time.RFC3339))
		}
		for _, key := range slices.Sorted(maps.Keys(card.DoneAt)) {
			fmt.Fprintf(stdout, "doneAt[%s] %s\n", key, card.DoneAt[key].Format(time.RFC3339))
		}
		return 0
	})
	return cmd
}

// newReplayRulesCommand is `legion replay rules`: every slot rule a replay can run
// (record.SlotRules), the daemon's own first, with its commit and each of its choices.
func newReplayRulesCommand(stdout, _ io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules [--json]",
		Short: "print every slot rule a replay can run, with its commit and each of its choices",
		Args:  noArguments,
	}
	asJSON := cmd.Flags().Bool("json", false, "print the rules as JSON")
	cmd.RunE = runs(func(context.Context, []string) int {
		rules := record.SlotRules()
		if *asJSON {
			views := make([]slotRuleView, 0, len(rules))
			for _, rule := range rules {
				views = append(views, slotRuleView(rule))
			}
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(views); err != nil {
				return 1
			}
			return 0
		}
		for i, rule := range rules {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			keeps := make([]string, 0, len(rule.KeepsSlotIn))
			for _, at := range rule.KeepsSlotIn {
				keeps = append(keeps, string(at))
			}
			fmt.Fprintf(stdout, "%s (commit %s)\n", rule.Name, rule.Commit)
			fmt.Fprintf(stdout, "  keeps a slot in: %s\n", strings.Join(keeps, ", "))
			fmt.Fprintf(stdout, "  a link wait gives the slot back: %t\n", rule.LinkWaitGivesBack)
			fmt.Fprintf(stdout, "  a children wait gives the slot back: %t\n", rule.ChildrenWaitGivesBack)
			fmt.Fprintf(stdout, "  a wait stops the phase under way: %t\n", rule.WaitStopsPhase)
			fmt.Fprintf(stdout, "  the line's order: %s\n", rule.Order)
			fmt.Fprintf(stdout, "  a start takes: %s\n", rule.Take)
			fmt.Fprintf(stdout, "  a conflicting head: %s\n", rule.Conflict)
			fmt.Fprintf(stdout, "  the slot goes back at the ready mark: %t\n", rule.ReleaseAtReady)
		}
		return 0
	})
	return cmd
}

// slotRuleView is a record.SlotRule as `legion replay rules --json` prints it: the rule's fields
// under JSON names, since the rule itself is no wire type.
type slotRuleView struct {
	Name                  string              `json:"name"`
	Commit                string              `json:"commit"`
	KeepsSlotIn           []phase.Phase       `json:"keepsSlotIn"`
	LinkWaitGivesBack     bool                `json:"linkWaitGivesBack"`
	ChildrenWaitGivesBack bool                `json:"childrenWaitGivesBack"`
	WaitStopsPhase        bool                `json:"waitStopsPhase"`
	Order                 record.LineOrder    `json:"order"`
	Take                  record.StartTake    `json:"take"`
	Conflict              record.ConflictPath `json:"conflict"`
	ReleaseAtReady        bool                `json:"releaseAtReady"`
}

// missingFlag is the first of names cmd was not given, "" when each was.
func missingFlag(cmd *cobra.Command, names ...string) string {
	for _, name := range names {
		if !cmd.Flags().Changed(name) {
			return name
		}
	}
	return ""
}

// replaySpan reads --from and --to as RFC 3339 times, refusing either that is not one, and a
// span that is empty, as a usage error.
func replaySpan(cmd *cobra.Command, from, to string) ([2]time.Time, int) {
	var span [2]time.Time
	for i, pair := range [][2]string{{"from", from}, {"to", to}} {
		at, err := time.Parse(time.RFC3339, pair[1])
		if err != nil {
			return span, refuse(cmd, "--%s %q is no RFC 3339 time", pair[0], pair[1])
		}
		span[i] = at
	}
	if !span[0].Before(span[1]) {
		return span, refuse(cmd, "--from %s is not before --to %s", from, to)
	}
	return span, 0
}

// designSteps is steps as --gate-steps takes them: comma-separated, or off for none.
func designSteps(steps []record.DesignStep) string {
	if len(steps) == 0 {
		return "off"
	}
	words := make([]string, 0, len(steps))
	for _, step := range steps {
		words = append(words, string(step))
	}
	return strings.Join(words, ",")
}

// parseDesignSteps reads --gate-steps as gates.design reads its value: off for no step,
// root-issues for the single step spec, and otherwise design steps, each once.
func parseDesignSteps(text string) ([]record.DesignStep, error) {
	if gate := config.DesignGate(strings.TrimSpace(text)); gate.Valid() {
		return gate.Steps(), nil
	}
	var steps []record.DesignStep
	for _, word := range strings.Split(text, ",") {
		step := record.DesignStep(strings.TrimSpace(word))
		if !step.Valid() {
			return nil, fmt.Errorf("%q is no design step (%s), and the value is not off or root-issues", step, record.StepWords())
		}
		for _, seen := range steps {
			if seen == step {
				return nil, fmt.Errorf("%q is named twice", step)
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// parsePart reads a --part, <from>,<to>,<release>,<commit>,<rule>.
func parsePart(text string) (replay.Part, error) {
	fields := strings.Split(text, ",")
	if len(fields) != 5 {
		return replay.Part{}, errors.New("a part is <from>,<to>,<release>,<commit>,<rule>")
	}
	var span [2]time.Time
	for i := range span {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[i]))
		if err != nil {
			return replay.Part{}, fmt.Errorf("%q is no RFC 3339 time", fields[i])
		}
		span[i] = at
	}
	return replay.Part{From: span[0], To: span[1], Release: strings.TrimSpace(fields[2]), Commit: strings.TrimSpace(fields[3]),
		Rule: strings.TrimSpace(fields[4])}, nil
}

// binaryCommit is the commit this binary was built from: the one the build linked in
// (revision), else the one Go's build recorded from the checkout; "" for neither.
func binaryCommit() string {
	if revision != "" {
		return revision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}

// dispatchToken is the Dispatch token a collect reads with, as the dispatch command finds one:
// the file --dispatch-token-file names, else the file DISPATCH_TOKEN_FILE names (one that is set
// and cannot be read, or is blank, is the answer, never a fall back), else DISPATCH_TOKEN. Each
// file's contents are trimmed; the token is never printed.
func dispatchToken(file string) (string, error) {
	if file != "" {
		return config.ReadSecretPointer("--dispatch-token-file", file)
	}
	if file := os.Getenv("DISPATCH_TOKEN_FILE"); file != "" {
		return config.ReadSecretPointer("DISPATCH_TOKEN_FILE", file)
	}
	if token := strings.TrimSpace(os.Getenv("DISPATCH_TOKEN")); token != "" {
		return token, nil
	}
	return "", errors.New("no Dispatch token: name its file with --dispatch-token-file, or set DISPATCH_TOKEN_FILE or DISPATCH_TOKEN")
}
