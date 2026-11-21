package harness

// The harness's reuse of earlier results and its refusals: a run reuses earlier results only under
// the digests of every input that changes them, a second `harness local` on one --out is refused,
// a credential goes over clear text only inside the cluster, and the pacer charges a call as
// modelpace does (dispatch://INFRA-3231).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
	"github.com/sjawhar/envoy/internal/testwait"
)

func TestCredentialURLRefusesClearTextOutsideTheCluster(t *testing.T) {
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{"https://gateway.example.com", true},
		{"http://127.0.0.1:4000", true},
		{"http://[::1]:4000/", true},
		{"http://localhost:4000", true},
		{"http://model-gateway.legion.svc:80", true},
		{"http://model-gateway.legion.svc.cluster.local", true},
		{"", true},
		{"http://gateway.example.com", false},
		{"http://10.0.0.7:4000", false},
		{"ftp://gateway.example.com", false},
		{"gateway.example.com", false},
	} {
		err := CredentialURL("--gateway", c.raw)
		if (err == nil) != c.ok {
			t.Errorf("CredentialURL(%q) = %v, want ok %t", c.raw, err, c.ok)
		}
	}
	code, _, stderr := runHarness("probe", "--gateway", "http://gateway.example.com", "--gateway-key-file", keyFile(t))
	if code != 2 || !strings.Contains(stderr, "is http to a host outside the cluster") {
		t.Errorf("harness probe at an http gateway outside the cluster = %d:\n%s", code, stderr)
	}
}

// WriterDigest names the refusal lines change 2's writer reads before the rules.
func TestWriterDigestChangesWithTheRefusalLines(t *testing.T) {
	before := WriterDigest()
	lines := writingRulesRefusalLines
	t.Cleanup(func() { writingRulesRefusalLines = lines })
	writingRulesRefusalLines = append(append([]string{}, lines...), "4. A line more.")
	if WriterDigest() == before {
		t.Error("WriterDigest is the same with a refusal line more")
	}
}

// The reviewer's digest moves with a prompt of the reviewer the instance is built from, and not
// with a test file, and the settings a change's run records hold it.
func TestTheReviewerDigestMovesWithAReviewerPrompt(t *testing.T) {
	if _, err := reviewerDigest(t.TempDir()); err == nil {
		t.Error("a root with no reviewer has a digest")
	}
	root := reviewerRoot(t)
	dir := filepath.Join(root, filepath.FromSlash(reviewerDir))
	before, err := reviewerDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules_test.go"), []byte("package reviewer // a test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if same, _ := reviewerDigest(root); same != before {
		t.Error("a test file moved the reviewer's digest")
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "rules.md"), []byte("Name the fact, and its owner.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := reviewerDigest(root)
	if err != nil || after == before {
		t.Errorf("a changed prompt gave %s (%v), the digest before it", after, err)
	}
	if got := (LocalInstance{ReviewerSHA256: after}).readSettings()["reviewer_sha256"]; got != after {
		t.Errorf("the settings a run records hold reviewer_sha256 %q, want %q", got, after)
	}
}

// The reviewer's digest moves with a file of the plain-words skill its rule sets and the written
// rules are cut from, and with a file of the writing check's bundle its detector rule set runs:
// each changes what the reviewer finds.
func TestTheReviewerDigestMovesWithThePlainWordsSkillAndTheBundle(t *testing.T) {
	root := reviewerRoot(t)
	before, err := reviewerDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		plainWordsDir + "/skill/SKILL.md",
		plainWordsDir + "/skill/references/patterns.md",
		"packages/envoy/internal/dispatch/writingcheck/reader/prompt.md",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("Changed.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		after, err := reviewerDigest(root)
		if err != nil || after == before {
			t.Errorf("a changed %s gave %s (%v), the digest before it", name, after, err)
		}
		before = after
	}
}

// reviewerRoot is a repository root that holds one file of each part reviewerDigest reads: the
// reviewer's code, a test of it and a prompt, package plainwords and its skill, and each part of
// the writing check's bundle.
func reviewerRoot(t *testing.T) string {
	t.Helper()
	files := map[string]string{
		reviewerDir + "/rules.go":                       "package reviewer\n",
		reviewerDir + "/rules_test.go":                  "package reviewer\n",
		reviewerDir + "/prompts/rules.md":               "Name the fact.\n",
		plainWordsDir + "/plainwords.go":                "package plainwords\n",
		plainWordsDir + "/skill/SKILL.md":               "Say what changed.\n",
		plainWordsDir + "/skill/references/patterns.md": "Name the owner.\n",
	}
	for _, part := range bundleParts {
		names := part.files
		if names == nil {
			names = []string{"part.go"}
		}
		for _, name := range append(slices.Clone(names), part.extra...) {
			files["packages/envoy/"+part.dir+"/"+name] = "package part\n"
		}
	}
	root := t.TempDir()
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// phrases-labels reuses its results only for the labels' bytes they were graded from: labels
// changed in place, in the same directory, are refused there.
func TestPhrasesLabelsRefusesLabelsChangedInPlace(t *testing.T) {
	dir := t.TempDir()
	cases := `{"event_id":1,"source_id":"a","field":"body","group":"g1","text":"This names the problem. The run is green."}` + "\n"
	labels := `{"event_id":1,"source_id":"a","family":"comment","sentences":[{"id":"s1","field":"body","text":"This names the problem.","verdict":"vacuous"},{"id":"s2","field":"body","text":"The run is green.","verdict":"not_vacuous"}]}` + "\n"
	for name, data := range map[string]string{"cases.jsonl": cases, "labels.jsonl": labels} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stand := modeltest.New(t, graderRules(t)...)
	out := t.TempDir()
	args := []string{"phrases-labels", "--labels", dir, "--out", out, "--gateway", stand.URL, "--gateway-key-file", keyFile(t), "--draws", "50"}
	if code, _, stderr := runHarness(args...); code != 0 {
		t.Fatalf("phrases-labels = %d: %s", code, stderr)
	}
	calls := len(stand.Requests())
	relabelled := strings.Replace(labels, `"verdict":"not_vacuous"`, `"verdict":"vacuous"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "labels.jsonl"), []byte(relabelled), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runHarness(args...)
	if code != 1 || !strings.Contains(stderr, "holds a run of other parameters") || !strings.Contains(stderr, "labels_sha256") {
		t.Errorf("phrases-labels of labels changed in place = %d: %s", code, stderr)
	}
	if n := len(stand.Requests()); n != calls {
		t.Errorf("the refused run made %d calls", n-calls)
	}
}

// A run of change 7a records the digest of the bundle it runs in process, and a run that
// continues its directory under another bundle is refused.
func TestASevenARunRecordsItsBundle(t *testing.T) {
	build, sessions := writeFixture(t)
	stand := modeltest.New(t, lockstepRules(t, nil)...)
	out := t.TempDir()
	if code, stderr := scoreSevenA(t, stand, build, sessions, out, "--model", laneA); code != 0 {
		t.Fatalf("the 7a run = %d:\n%s", code, stderr)
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := BundleDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, "run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var params RunParams
	if err := json.Unmarshal(data, &params); err != nil || params.CheckBundleSHA256 != want {
		t.Fatalf("run.json records check_bundle_sha256 %q (%v), want the bundle's %s", params.CheckBundleSHA256, err, want)
	}
	params.CheckBundleSHA256 = strings.Repeat("0", 64)
	writeJSONFile(t, path, params)
	if code, stderr := scoreSevenA(t, stand, build, sessions, out, "--model", laneA); code != 1 || !strings.Contains(stderr, "holds a run of other parameters") {
		t.Errorf("a 7a run continuing a directory of another bundle = %d:\n%s", code, stderr)
	}
}

// A second `harness local` on the --out a first start holds is refused at once, even before the
// first instance is up; and a start removes, when it ends, only the local.json it made, not a
// file another start wrote there since.
func TestASecondLocalStartOnOneOutIsRefused(t *testing.T) {
	bin, out := t.TempDir(), t.TempDir()
	pids := filepath.Join(t.TempDir(), "pids")
	script := "#!/bin/sh\necho $$ >> \"$HARNESS_TEST_BUN_PID\"\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HARNESS_TEST_BUN_PID", pids)
	t.Cleanup(func() {
		data, _ := os.ReadFile(pids)
		for _, line := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(line); err == nil {
				syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	args := []string{"local", "--out", out, "--pg-dsn", "postgres://harness@127.0.0.1:1/postgres", "--database", "dispatch_harness_1", "--repo", t.TempDir()}
	first, stopFirst := context.WithCancel(context.Background())
	defer stopFirst()
	firstDone := make(chan int, 1)
	go func() {
		var stdout, stderr strings.Builder
		firstDone <- Run(first, args, &stdout, &stderr)
	}()
	testwait.Eventually(t, "the first start's instance to run", func() bool {
		data, _ := os.ReadFile(pids)
		return len(strings.Fields(string(data))) == 1
	})
	// The bound ends a second start that was let through; one refused returns at once.
	second, stopSecond := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopSecond()
	var stdout, stderr strings.Builder
	if code := Run(second, args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "another `harness local` holds this --out") {
		t.Errorf("a second start on the held --out = %d:\n%s", code, stderr.String())
	}
	// Another start's local.json takes the place of the first's: the first leaves it as it ends.
	held := filepath.Join(out, "local.json")
	if err := os.Remove(held); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(held, []byte(`{"base_url":"http://127.0.0.1:4200"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stopFirst()
	<-firstDone
	if data, err := os.ReadFile(held); err != nil || string(data) != `{"base_url":"http://127.0.0.1:4200"}` {
		t.Errorf("the first start, as it ended, removed or changed the local.json another wrote (%q, %v)", data, err)
	}
}

// A `harness local` start stopped (Ctrl-C) before its instance is up fails: it started no
// instance, as it was asked to. A stop once the instance is up is how it ends.
func TestALocalStartStoppedBeforeItsInstanceIsUpFails(t *testing.T) {
	bin, out := t.TempDir(), t.TempDir()
	pids := filepath.Join(t.TempDir(), "pids")
	script := "#!/bin/sh\necho $$ >> \"$HARNESS_TEST_BUN_PID\"\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HARNESS_TEST_BUN_PID", pids)
	t.Cleanup(func() {
		data, _ := os.ReadFile(pids)
		for _, line := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(line); err == nil {
				syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan int, 1)
	var stderr strings.Builder
	go func() {
		var stdout strings.Builder
		done <- Run(ctx, []string{"local", "--out", out, "--pg-dsn", "postgres://harness@127.0.0.1:1/postgres", "--database", "dispatch_harness_1", "--repo", t.TempDir()}, &stdout, &stderr)
	}()
	testwait.Eventually(t, "the start's instance to run", func() bool {
		data, _ := os.ReadFile(pids)
		return len(strings.Fields(string(data))) == 1
	})
	stop()
	if code := <-done; code != 1 || !strings.Contains(stderr.String(), "before the instance was up") {
		t.Errorf("a start stopped before its instance was up = %d:\n%s", code, stderr.String())
	}
}

// Every harness command refuses, with exit 2 and before it writes anything, an output that is one
// of its inputs: a file it writes, or one it writes in its output directory, that is a file
// another of its flags reads (a key file, --case-list, the build's cases, --planted, a run's
// files, local.json). The input is left as it was.
func TestEveryHarnessCommandRefusesAnOutputThatIsAnInput(t *testing.T) {
	build, sessions := writeFixture(t)
	dir := t.TempDir()
	// file writes an input at name under dir and answers its path.
	file := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	at := func(name string) string { return filepath.Join(dir, name) }
	key := file("key")
	// The gateway and Postgres are a port that answers nothing, so a command that went on past
	// its refusal fails at once.
	gateway := []string{"--gateway", "http://127.0.0.1:1", "--gateway-key-file", key}
	builtCases := filepath.Join(build, casesFile)
	for _, c := range []struct {
		args         []string
		input, wants string
	}{
		{append([]string{"probe", "--out", key}, gateway...), key, "--out names the --gateway-key-file file"},
		{append([]string{"score", "--cases", build, "--sessions", sessions, "--out", at("score"), "--case-list", file("score/scores.md")}, gateway...), at("score/scores.md"), "scores.md of --out is the --case-list file"},
		{append([]string{"score", "--part", PartHeldOut, "--commit", "c0ffee", "--cases", build, "--sessions", sessions, "--out", at("held"), "--exposure-log", builtCases}, gateway...), builtCases, "--exposure-log names cases.jsonl.gz of --cases"},
		{append([]string{"grader-test", "--planted", file("grader/grader-test.json"), "--cases", build, "--sessions", sessions, "--out", at("grader")}, gateway...), at("grader/grader-test.json"), "grader-test.json of --out is the --planted file"},
		{[]string{"compare", "--a", filepath.Dir(file("a/run.json")), "--b", at("a"), "--out", at("a/run.json")}, at("a/run.json"), "--out names run.json of --a"},
		{[]string{"phrases-labels", "--labels", at("labels"), "--out", at("phrases"), "--gateway-key-file", file("phrases/results.jsonl")}, at("phrases/results.jsonl"), "results.jsonl of --out is the --gateway-key-file file"},
		{[]string{"asked-words", "--run", filepath.Dir(file("run/cases.jsonl")), "--cases", build, "--out", at("run/cases.jsonl")}, at("run/cases.jsonl"), "--out names cases.jsonl of --run"},
		{[]string{"local", "--out", at("local"), "--pg-dsn", "postgres://127.0.0.1:1/x", "--gateway-key-file", file("local/local.json")}, at("local/local.json"), "local.json of --out is the --gateway-key-file file"},
		{[]string{"load", "--local", file("load/loaded.jsonl"), "--cases", build, "--verdicts", key, "--out", at("load")}, at("load/loaded.jsonl"), "loaded.jsonl of --out is the --local file"},
	} {
		before, err := os.ReadFile(c.input)
		if err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runHarness(c.args...)
		if code != 2 || !strings.Contains(stderr, c.wants) {
			t.Errorf("harness %v = %d:\n%s\nwant 2 and %q", c.args, code, stderr, c.wants)
		}
		if after, err := os.ReadFile(c.input); err != nil || string(after) != string(before) {
			t.Errorf("harness %s changed its input %s (%v)", c.args[0], c.input, err)
		}
	}
}

// writeJSON, and each write ReplaceFile makes, goes through a file of a name of its own beside its
// output: another run's file under the name the temporary file had before (<path>.tmp) is left
// as it was, and a write that fails leaves no file of its own behind.
func TestReplaceFileLeavesAnotherRunsFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")
	if err := os.WriteFile(path+".tmp", []byte("another run's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, map[string]int{"cases": 1}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{\n  \"cases\": 1\n}\n" {
		t.Errorf("writeJSON wrote %q (%v)", data, err)
	}
	if data, err := os.ReadFile(path + ".tmp"); err != nil || string(data) != "another run's\n" {
		t.Errorf("writeJSON left another run's run.json.tmp as %q (%v)", data, err)
	}
	// A rename over a directory that holds a file fails.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(blocked, []byte("x"), 0o600); err == nil {
		t.Error("a write over a directory that holds a file answered no error")
	}
	for _, own := range []string{path + ".*.tmp", blocked + ".*.tmp"} {
		if left, _ := filepath.Glob(own); len(left) != 0 {
			t.Errorf("the writes left %v", left)
		}
	}
	a, err := CreateBeside(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(a.Name())
	defer a.Close()
	b, err := CreateBeside(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(b.Name())
	defer b.Close()
	if info, err := a.Stat(); a.Name() == b.Name() || filepath.Dir(a.Name()) != dir || err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("two files beside %s: %s and %s (%v)", path, a.Name(), b.Name(), err)
	}
}
