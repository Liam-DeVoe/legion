package main

// The commands of package main that reuse, refuse or fail a run: a run that reuses earlier results
// only under its own inputs, a run whose calls failed or stopped that fails, an output a
// repository or an input does not take, and a claim made after every refusal that reads no write
// (dispatch://INFRA-3231).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/writingcheck/eval"
)

// labelFixture is a cases file of one comment, a definition and a key file in dir.
func labelFixture(t *testing.T, dir, text string) (cases, definition, key string) {
	t.Helper()
	cases, definition, key = filepath.Join(dir, "cases.jsonl"), filepath.Join(dir, "definition.md"), filepath.Join(dir, "key")
	writeJSONLines(t, cases, Case{EventID: 7, SourceID: "c7", Surface: "comment", Field: "body", Text: text})
	for path, data := range map[string]string{definition: "A verb phrase is vacuous when it states no fact.\n", key: "stub-key\n"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cases, definition, key
}

// labelArgs are label's flags against the stub.
func labelArgs(stub *stubModel, cases, definition, key, out string, extra ...string) []string {
	return append([]string{"label", "--in", cases, "--definition", definition, "--out", out, "--kind", "openai-chat",
		"--endpoint", stub.server.URL + "/", "--key-file", key, "--concurrency", "1"}, extra...)
}

// The top-level score writes outputs.jsonl, which holds sentences of the labelled texts: an --out
// inside a repository is refused before any input is read, and nothing is made there.
func TestScoreRefusesAnOutInsideARepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := newStubModel(t, containsAny("names the"), nil)
	inside := filepath.Join(repo, "data", "out")
	code, _, stderr := runCmd(t, append([]string{"score", "--labels", filepath.Join(t.TempDir(), "labels.jsonl"), "--stratum", stratumReal, "--out", inside}, stub.args(t, t.TempDir())...)...)
	if code != 2 || !strings.Contains(stderr, " is inside the repository at "+repo+": its files hold Dispatch text") {
		t.Errorf("score --out inside a repository = %d:\n%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused score made %s (%v)", filepath.Join(repo, "data"), err)
	}
}

// A final-test score whose --out cannot be made fails before it claims the set: the exposure log
// holds no run, so the set is not spent on a run that read nothing.
func TestAFinalTestWhoseOutCannotBeMadeSpendsNoSet(t *testing.T) {
	dir := t.TempDir()
	labels, manifest := fixtureLabels(t, dir)
	blocker := filepath.Join(dir, "a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "exposure.jsonl")
	stub := newStubModel(t, containsAny("names the"), nil)
	args := append([]string{"score", "--labels", labels, "--planted", manifest, "--stratum", stratumReal, "--out", filepath.Join(blocker, "out"),
		"--root", "../..", "--final-test", "--set-digest", strings.Repeat("ab", 32), "--seed", "42", "--exposure-log", log}, stub.args(t, dir)...)
	code, _, stderr := runCmd(t, args...)
	if code != 1 || !strings.Contains(stderr, "not a directory") {
		t.Errorf("a final test whose --out cannot be made = %d:\n%s", code, stderr)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the run that read no write wrote the exposure log (%v)", err)
	}
	if stub.calls.Load() != 0 {
		t.Error("the refused run called the model")
	}
}

// label --resume keeps a label only where the run's definition, model, effort and chunk, and the
// write's text, are those it was made with: a resume after the definition or the text changed is
// refused before any call, and a resume under the same ones keeps the label and calls nothing.
func TestLabelResumeRefusesLabelsOfOtherInputs(t *testing.T) {
	dir := t.TempDir()
	text := "The fix names the risk."
	cases, definition, key := labelFixture(t, dir, text)
	rubric, err := os.ReadFile(definition)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := labelJobs([]Case{{EventID: 7, SourceID: "c7", Surface: "comment", Field: "body", Text: text}}, func(string, int64) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	record := func(params, input string) labelRecord {
		return labelRecord{EventID: 7, SourceID: "c7", Family: "comment", WriteLabel: writePass, Params: params, Input: input,
			Sentences: []sentenceLabel{{ID: "s1", Field: "body", Text: text, Verdict: verdictNotVacuous, Shape: "none"}}, Calls: []labelCall{}}
	}
	same := labelParams(labelPreamble+string(rubric), "claude-opus-5-5", "off", 60)
	other := labelParams(labelPreamble+"An earlier definition.\n", "claude-opus-5-5", "off", 60)
	stub := newStubModel(t, containsAny("names the"), nil)
	for _, c := range []struct {
		name   string
		record labelRecord
		code   int
		want   string
	}{
		{"another definition", record(other, jobs[0].input), 2, "made with another definition, model, effort or --chunk"},
		{"another text", record(same, sha256Hex([]byte("another text"))), 2, "whose text or issue title in --in is not the one they were labelled from"},
		{"the same inputs", record(same, jobs[0].input), 0, "1 writes, 1 already labelled, 0 to label"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "labels.jsonl")
			writeJSONLines(t, out, c.record)
			before := stub.calls.Load()
			code, _, stderr := runCmd(t, labelArgs(stub, cases, definition, key, out, "--resume")...)
			if code != c.code || !strings.Contains(stderr, c.want) {
				t.Errorf("label --resume = %d, want %d and %q:\n%s", code, c.code, c.want, stderr)
			}
			if stub.calls.Load() != before {
				t.Errorf("the resume made %d calls", stub.calls.Load()-before)
			}
		})
	}
}

// label refuses an --out that names its --in or its --titles file, which the labels would be
// renamed over.
func TestLabelRefusesAnOutThatIsItsInput(t *testing.T) {
	dir := t.TempDir()
	cases, definition, key := labelFixture(t, dir, "The fix names the risk.")
	before, err := os.ReadFile(cases)
	if err != nil {
		t.Fatal(err)
	}
	stub := newStubModel(t, containsAny("names the"), nil)
	titles := filepath.Join(dir, "titles.jsonl")
	if err := os.WriteFile(titles, before, 0o600); err != nil {
		t.Fatal(err)
	}
	for flag, out := range map[string]string{"--in": cases, "--titles": titles} {
		code, _, stderr := runCmd(t, labelArgs(stub, cases, definition, key, out, "--resume", "--titles", titles)...)
		if code != 2 || !strings.Contains(stderr, "--out names the "+flag+" file") {
			t.Errorf("label --out %s = %d:\n%s", flag, code, stderr)
		}
	}
	if after, err := os.ReadFile(cases); err != nil || string(after) != string(before) {
		t.Errorf("the refused label changed its --in (%v)", err)
	}
	if stub.calls.Load() != 0 {
		t.Error("the refused label called the model")
	}
}

// A label run in which a write's calls failed leaves that write labelled "error", counts the
// failed calls in its summary and exits 1, so the run does not read as complete.
func TestALabelRunWithFailedCallsExitsOne(t *testing.T) {
	dir := t.TempDir()
	cases, definition, key := labelFixture(t, dir, "The fix names the risk.")
	// The stub answers the reader's schema, which is not the labeller's: every reply is refused.
	stub := newStubModel(t, containsAny("names the"), nil)
	out := filepath.Join(dir, "labels.jsonl")
	code, stdout, stderr := runCmd(t, labelArgs(stub, cases, definition, key, out)...)
	if code != 1 || !strings.Contains(stderr, "1 of 1 writes have no label, as their calls failed") || !strings.Contains(stdout, "failed 1)") {
		t.Errorf("a label run whose calls failed = %d:\n%s%s", code, stdout, stderr)
	}
}

// probeArgs are probe-model's flags against the stub.
func probeArgs(t *testing.T, stub *stubModel, extra ...string) []string {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("stub-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return append([]string{"probe-model", "--kind", "openai-chat", "--endpoint", stub.server.URL + "/", "--deployment", "stub-deployment",
		"--key-file", key, "--deadline", "10s"}, extra...)
}

// probe-model exits 0 when every call read its candidates, and 1 when a call failed, when it
// stopped with fewer calls than asked, or when --out could not take a record.
func TestProbeModelFailsOnAFailedCallAStopOrAWriteError(t *testing.T) {
	out := filepath.Join(t.TempDir(), "calls.jsonl")
	if code, stdout, stderr := runCmd(t, probeArgs(t, newStubModel(t, containsAny("names the"), nil), "--calls", "3", "--out", out)...); code != 0 {
		t.Errorf("a probe whose calls all answered = %d:\n%s%s", code, stdout, stderr)
	}
	if lines := readJSONL[probeCall](t, out); len(lines) != 3 {
		t.Errorf("--out holds %d records, want 3", len(lines))
	}
	failing := newStubModel(t, containsAny("names the"), func(string) bool { return true })
	if code, _, stderr := runCmd(t, probeArgs(t, failing, "--calls", "2")...); code != 1 || !strings.Contains(stderr, "2 of 2 calls failed") {
		t.Errorf("a probe whose calls failed = %d:\n%s", code, stderr)
	}
	if _, err := os.Stat("/dev/full"); err == nil {
		code, _, stderr := runCmd(t, probeArgs(t, newStubModel(t, containsAny("names the"), nil), "--calls", "2", "--out", "/dev/full")...)
		if code != 1 || !strings.Contains(stderr, "writing /dev/full") {
			t.Errorf("a probe whose --out takes no record = %d:\n%s", code, stderr)
		}
	}
	// The first call's reply cancels the run's context: the probe stops after one call of three.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopping := newStubModel(t, func(string) bool { cancel(); return false }, nil)
	var stdout, stderr strings.Builder
	if code := run(ctx, probeArgs(t, stopping, "--calls", "3"), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "stopped (context canceled) after 1 of 3 calls") {
		t.Errorf("a probe stopped after its first call = %d:\n%s", code, stderr.String())
	}
}

// validate refuses an --exposure-log that does not exist once the description names the
// final-test set, and prints the log it read: a mistyped path would otherwise read as a log in
// which no run read the set.
func TestValidateRefusesAMissingExposureLog(t *testing.T) {
	f := newValidFixture(t)
	f.d.FinalTest = &eval.FinalTest{Pending: eval.Pending{Pending: true, Reason: "later"}}
	f.runs = nil
	f.write(t)
	code, stdout, _ := runCmd(t, "validate", "--description", f.desc, "--exposure-log", f.log)
	if code != 0 || !strings.Contains(stdout, "exposure log: "+f.log+", 0 runs") {
		t.Fatalf("a description before its claim with an empty log = %d:\n%s", code, stdout)
	}
	if err := os.Remove(f.log); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runCmd(t, "validate", "--description", f.desc, "--exposure-log", f.log)
	if code != 1 || !strings.Contains(stdout, "the --exposure-log file does not exist, and the description names the final-test set") ||
		!strings.Contains(stdout, f.log+" does not exist") {
		t.Errorf("a missing exposure log = %d:\n%s", code, stdout)
	}
}

// A regress case whose reader call failed is a fault, not a disagreement: the classes count the
// cases read, and a run with a fault exits 1 under any bound.
func TestRegressCountsAFailedCallApartAndFails(t *testing.T) {
	dir := t.TempDir()
	cases := []regressCase{
		{ID: "r1", Body: sentA + " " + sentB, Expect: []regressVerdict{{sentA, true}, {sentB, false}}},
		{ID: "r2", Body: sentH, Expect: []regressVerdict{{sentH, false}}},
		{ID: "r3", Body: sentC + "\n\n" + sentM, Expect: []regressVerdict{{sentC, true}, {sentM, false}}},
	}
	path := filepath.Join(dir, "cases.jsonl")
	if _, err := writeJSONL(path, cases); err != nil {
		t.Fatal(err)
	}
	failing := newStubModel(t, containsAny("names the"), containsAny("shows the"))
	out := filepath.Join(dir, "regress.json")
	code, stdout, stderr := runCmd(t, append([]string{"regress", "--cases", path, "--repeat-bound", "1", "--out", out}, failing.args(t, dir)...)...)
	if code != 1 || !strings.Contains(stderr, "1 of 3 cases were not read whole") || !strings.Contains(stdout, "- r3: reader_error, 2 expected sentences unread") {
		t.Errorf("regress with a failed call at a bound of 100%% = %d:\n%s%s", code, stdout, stderr)
	}
	var report regressReport
	data, _ := os.ReadFile(out)
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if c := report.Classes; len(report.Faults) != 1 || len(report.Misses) != 0 || c[0].N != 1 || c[1].N != 2 || report.Pass {
		t.Errorf("the report: classes %+v, faults %+v, misses %+v, pass %t", c, report.Faults, report.Misses, report.Pass)
	}
}

// readers --state continues a replayed state only from the Dispatch it replayed: another
// DISPATCH_URL is refused before any read.
func TestReadersRefusesAStateOfAnotherDispatch(t *testing.T) {
	dir := t.TempDir()
	in, state := filepath.Join(dir, "cases.jsonl"), filepath.Join(dir, "state.json")
	writeJSONLines(t, in, Case{EventID: 3, SourceID: "c3", Surface: "comment", Field: "body", Text: "The run is green."})
	s := newReplayState(0)
	s.DispatchURL, s.Through, s.Events = "http://127.0.0.1:9", 2, 2
	if err := saveReplayState(state, s); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISPATCH_URL", "http://127.0.0.1:1")
	t.Setenv("DISPATCH_TOKEN_FILE", filepath.Join(dir, "token"))
	code, _, stderr := runCmd(t, "readers", "--in", in, "--out", filepath.Join(dir, "out.jsonl"), "--log-after", "0", "--log-through", "5", "--state", state)
	if code != 2 || !strings.Contains(stderr, `was replayed from Dispatch "http://127.0.0.1:9", not DISPATCH_URL http://127.0.0.1:1`) {
		t.Errorf("readers --state of another Dispatch = %d:\n%s", code, stderr)
	}
}

// fetch --resume continues cases read from the same Dispatch after the same event only: a partial
// file of another Dispatch, or one whose fetch recorded neither, is refused before any read.
func TestFetchResumeRefusesCasesOfAnotherDispatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DISPATCH_URL", "http://127.0.0.1:1")
	t.Setenv("DISPATCH_TOKEN_FILE", filepath.Join(dir, "token"))
	out := filepath.Join(dir, "cases.jsonl")
	writeJSONLines(t, out+".partial", Case{EventID: 3, SourceID: "c3", Surface: "comment", Field: "body", Text: "The run is green."})
	args := []string{"fetch", "--after", "0", "--through", "10", "--out", out, "--resume"}
	code, _, stderr := runCmd(t, args...)
	if code != 2 || !strings.Contains(stderr, "which names the Dispatch and the --after they were read from, does not exist") {
		t.Errorf("fetch --resume of a partial file with no record = %d:\n%s", code, stderr)
	}
	writeJSONLines(t, fetchParamsPath(out+".partial"), fetchParams{DispatchURL: "http://127.0.0.1:9", After: 0})
	code, _, stderr = runCmd(t, args...)
	if code != 2 || !strings.Contains(stderr, "was read from http://127.0.0.1:9 after event 0; this run reads http://127.0.0.1:1 after event 0") {
		t.Errorf("fetch --resume of another Dispatch's cases = %d:\n%s", code, stderr)
	}
}

// A dataset fetch's work directory is reused only for the same end, owner and Dispatch.
func TestDatasetFetchWorkIsReusedFromOneDispatchOnly(t *testing.T) {
	dir := t.TempDir()
	if err := dsCheckWorkEnd(dir, fixtureEnd, fixtureOwner, "http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if err := dsCheckWorkEnd(dir, fixtureEnd, fixtureOwner, "http://127.0.0.1:9"); err != nil {
		t.Errorf("the same fetch again: %v", err)
	}
	if err := dsCheckWorkEnd(dir, fixtureEnd, fixtureOwner, "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), `from Dispatch "http://127.0.0.1:9"`) {
		t.Errorf("a fetch of another Dispatch into the work directory: %v", err)
	}
}

// classify keeps an earlier verdict only where it was made with this run's prompt, model and
// input; and a text whose last line is an error has no verdict, though an earlier line held one.
func TestClassifyKeepsOnlyAVerdictOfItsOwnInputs(t *testing.T) {
	v := dsVerdict{Text: "t1", PromptSHA256: "p", RequestedModel: "m", InputSHA256: "i", Verdict: "about_words"}
	for _, c := range []struct {
		name                 string
		prompt, model, input string
		keep                 bool
	}{
		{"the same", "p", "m", "i", true},
		{"another prompt", "p2", "m", "i", false},
		{"another model", "p", "m2", "i", false},
		{"another input", "p", "m", "i2", false},
	} {
		if got := currentVerdict(v, c.prompt, c.model, c.input); got != c.keep {
			t.Errorf("%s: kept %t, want %t", c.name, got, c.keep)
		}
	}
	path := filepath.Join(t.TempDir(), "verdicts.jsonl")
	writeJSONLines(t, path, v, dsVerdict{Text: "t1", Error: "the call failed"}, dsVerdict{Text: "t2", Verdict: "plain"})
	got, _, err := readVerdicts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["t1"]; ok || got["t2"].Verdict != "plain" {
		t.Errorf("the verdicts read: %+v", got)
	}
}
