package harness

// The tests of test handoff version 4's findings and the class audit's instances in the harness: a
// run reuses earlier results only under the digests of every input that changes them, a second
// `harness local` on one --out is refused, a credential goes over clear text only inside the
// cluster, and the pacer charges a call as modelpace does.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// The reviewer's digest moves with a prompt of the reviewer the instance is built from, and the
// settings a change's run records hold it.
func TestTheReviewerDigestMovesWithAReviewerPrompt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(reviewerDir))
	if _, err := reviewerDigest(root); err == nil {
		t.Error("a root with no reviewer has a digest")
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"rules.go": "package reviewer\n", "prompts/rules.md": "Name the fact.\n", "rules_test.go": "package reviewer\n"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
