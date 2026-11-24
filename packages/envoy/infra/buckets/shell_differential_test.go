package buckets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
)

// The differential tests below are the cross-family review's harness (review 6033098625, the full
// report's four must-fixes: line continuation, assignment scope, trims, the reading cap), adopted
// into the package. Real `/bin/sh` decides each case: a script that starts envoy-listener or
// envoy-dispatch is a writer, and readShell must name that writer (0 misses); a script that starts
// another program is not, and readShell must name neither (0 false positives). The generator is the
// review's, unchanged; only the detector call is this package's shellNames. Where /bin/sh is absent
// the differential is skipped, named in the run line.

// reviewShellCase is one script the differential runs, its env and its positional words.
type reviewShellCase struct {
	name, script string
	vars         map[string]string
	pos          []string
}

// requireShell skips a test where /bin/sh cannot run it, so the differential's silence is never read
// as a pass where no shell decided.
func requireShell(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh is absent (%v): the differential needs a real shell to decide each case", err)
	}
}

// reviewShellRun runs c's script under /bin/sh with the writer stand-ins on path, and returns what it
// printed: the base name of the program exec started, since each stand-in prints its own name.
func reviewShellRun(t *testing.T, c reviewShellCase, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := append([]string{"-c", c.script, "sh"}, c.pos...)
	cmd := exec.CommandContext(ctx, "/bin/sh", args...)
	cmd.Env = []string{"PATH=" + path, "LC_ALL=C"}
	for name, value := range c.vars {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// reviewLookup is a container's env for the differential: a name the case sets is read with its
// value, any other name is unset, as a pod that does not set it leaves it (imageMayVary false).
func reviewLookup(vars map[string]string) func(string) variable {
	return func(name string) variable {
		v, ok := vars[name]
		return variable{value: v, set: ok, read: ok}
	}
}

// reviewNamed reports whether readShell names a writer for words: a writer it reads, or a reading it
// cannot finish (shellNames' undecided), which decides 'writer'.
func reviewNamed(words []string, vars map[string]string) (string, bool) {
	binary, undecided := shellNames(words, reviewLookup(vars), false)
	return binary, binary != "" || undecided
}

func TestReviewShellDifferential(t *testing.T) {
	requireShell(t)
	dir := t.TempDir()
	for _, binary := range []string{listenerBinary, dispatchBinary, "nats-server", "envoy-hub"} {
		body := "#!/bin/sh\nprintf '%s\\n' '" + binary + "'\n"
		if err := os.WriteFile(filepath.Join(dir, binary), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// The two controls exercise the same shell and detector path, including split-word composition.
	for _, c := range []reviewShellCase{
		{name: "positive", script: `R=dispatch; exec envoy-$R`},
		{name: "negative", script: `R=hub; exec envoy-$R`},
	} {
		got := reviewShellRun(t, c, dir)
		binary, _ := reviewNamed([]string{"-c", c.script, "sh"}, nil)
		if c.name == "positive" && (got != dispatchBinary || binary != dispatchBinary) {
			t.Fatalf("positive control shell=%q named=%q", got, binary)
		}
		if c.name == "negative" && (got != "envoy-hub" || binary != "") {
			t.Fatalf("negative control shell=%q named=%q", got, binary)
		}
	}
	var cases []reviewShellCase
	for _, role := range []string{"listener", "dispatch", "hub"} {
		for _, expr := range []string{`$ROLE`, `${ROLE}`, `${ROLE:-hub}`} {
			for _, quoted := range []bool{false, true} {
				word := "envoy-" + expr
				if quoted {
					word = `"` + word + `"`
				}
				cases = append(cases, reviewShellCase{name: "env-" + role + "-" + word, script: "exec " + word, vars: map[string]string{"ROLE": role}})
			}
		}
		for _, prefix := range []string{"ROLE=" + role + "; ", "ROLE='" + role + "'; ", "ROLE=\"" + role + "\"; ", "export ROLE=" + role + "; ", "ROLE=unused; ROLE=" + role + "; ", "ROLE=" + role + "\n"} {
			cases = append(cases, reviewShellCase{name: "literal-" + prefix, script: prefix + `exec "envoy-${ROLE}"`})
		}
		cases = append(cases, reviewShellCase{name: "default-unset-" + role, script: `exec "envoy-${ROLE:-` + role + `}"`})
		cases = append(cases, reviewShellCase{name: "default-empty-" + role, script: `exec "envoy-${ROLE:-` + role + `}"`, vars: map[string]string{"ROLE": ""}})
		cases = append(cases, reviewShellCase{name: "default-assigned-empty-" + role, script: `ROLE=''; exec "envoy-${ROLE:-` + role + `}"`})
		for index := 1; index <= 9; index++ {
			pos := make([]string, index)
			for i := range pos {
				pos[i] = "unused"
			}
			pos[index-1] = role
			cases = append(cases, reviewShellCase{name: fmt.Sprintf("pos-%d-%s", index, role), script: fmt.Sprintf(`exec "envoy-$%d"`, index), pos: pos})
		}
		for _, script := range []string{
			"exec envoy-\\\n" + role,
			"ROLE=" + role[:2] + "\\\n" + role[2:] + `; exec "envoy-$ROLE"`,
			"exec \"envoy-\\\n" + role + "\"",
			`ROLE=` + role + `; EMPTY=''; exec "envoy-${EMPTY}${ROLE}"`,
			`ROLE=` + role[:2] + `; ROLE=${ROLE}` + role[2:] + `; exec envoy-$ROLE`,
			`export ROLE="` + role + `"; exec envoy-$ROLE`,
			`ROLE="${ROLE:-` + role + `}"; exec envoy-$ROLE`,
			`exec "envoy-${ROLE:-${FALLBACK:-` + role + `}}"`,
		} {
			cases = append(cases, reviewShellCase{name: "extra-" + script, script: script})
		}
	}
	misses, falsepos, started := 0, 0, 0
	for i, c := range cases {
		actual := reviewShellRun(t, c, dir)
		words := append([]string{"-c", c.script, "sh"}, c.pos...)
		binary, named := reviewNamed(words, c.vars)
		writer := actual == listenerBinary || actual == dispatchBinary
		if writer {
			started++
		}
		if writer && !named {
			misses++
			t.Logf("MISS case=%d script=%q env=%v pos=%q sh=%q named=%q", i, c.script, c.vars, c.pos, actual, binary)
		}
		if !writer && named {
			falsepos++
			t.Logf("FALSE_POSITIVE case=%d script=%q env=%v pos=%q sh=%q named=%q", i, c.script, c.vars, c.pos, actual, binary)
		}
	}
	t.Logf("TOTAL cases=%d actual_writers=%d misses=%d false_positives=%d", len(cases), started, misses, falsepos)
	if misses > 0 {
		t.Errorf("%d supported-form shells started a writer but readShell names neither", misses)
	}
	if falsepos > 0 {
		t.Errorf("%d shells started no writer but readShell names one", falsepos)
	}
}

// TestReviewShellTermination holds that a self-referencing or growing value, in the env or in the
// script, does not make readShell loop: it returns within a wall-clock bound, its reading bounded.
func TestReviewShellTermination(t *testing.T) {
	for _, c := range []reviewShellCase{
		{name: "env-self", script: `exec "$A"`, vars: map[string]string{"A": "$A"}},
		{name: "env-mutual", script: `exec "$A"`, vars: map[string]string{"A": "$B", "B": "$A"}},
		{name: "env-growing", script: `exec "$A"`, vars: map[string]string{"A": "${A}${A}"}},
		{name: "assignment-self", script: `A='$A'; exec "$A"`},
		{name: "assignment-mutual", script: `A='$B'; B='$A'; exec "$A"`},
		{name: "assignment-growing", script: `A='$A$A'; exec "$A"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan time.Duration, 1)
			go func() {
				start := time.Now()
				readShell([]string{"-c", c.script}, reviewLookup(c.vars), false)
				done <- time.Since(start)
			}()
			select {
			case took := <-done:
				t.Logf("returned in %s", took)
			case <-time.After(10 * time.Second):
				t.Fatal("readShell did not return in 10 s")
			}
		})
	}
}

// TestReviewLineContinuationBlocksWriter holds the first must-fix: a backslash before a line break
// joins the lines, so exec envoy-\<newline>listener starts a writer and recreate refuses beside it.
func TestReviewLineContinuationBlocksWriter(t *testing.T) {
	for _, role := range []string{"listener", "dispatch"} {
		c := fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", "exec envoy-\\\n" + role}}
		p := fakePod{namespace: "envoy", name: "pod-continuation", containers: []fakeContainer{c}}
		bucket := bus.CIStateBucket
		if role == "listener" {
			bucket = bus.KnownSessionBucket
		}
		code, stdout, stderr := recreateIn(t, &world{podsIn: map[string]string{"envoy": podsJSON(t, p)}}, bucket, "envoy")
		t.Logf("role=%s recreate_code=%d stdout=%q stderr=%q", role, code, stdout, stderr)
		if code != 1 || !strings.Contains(stderr, "pod-continuation") || strings.Contains(stdout, "deleted") {
			t.Errorf("recreate did not refuse beside shell-started %s", role)
		}
	}
}

// TestReviewAssignmentScope holds the second and third must-fixes: a prefix assignment on a command
// lasts only for it, a separate or exported one lasts, a conditional one adds a value and replaces
// none, and a trim reads the program's base name. The named writer is the one /bin/sh runs.
func TestReviewAssignmentScope(t *testing.T) {
	requireShell(t)
	dir := t.TempDir()
	for _, binary := range []string{listenerBinary, dispatchBinary, "envoy-hub"} {
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nprintf '%s\\n' '"+binary+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []reviewShellCase{
		{name: "prefix-before-exec", script: `ROLE=dispatch exec envoy-$ROLE`, vars: map[string]string{"ROLE": "listener"}},
		{name: "temporary-command-assignment", script: `ROLE=listener; ROLE=dispatch true; exec envoy-$ROLE`},
		{name: "control-separate-assignment", script: `ROLE=listener; ROLE=dispatch; exec envoy-$ROLE`},
		{name: "control-export-assignment", script: `ROLE=listener; export ROLE=dispatch; exec envoy-$ROLE`},
		{name: "conditional-assignment", script: `[ -n "$CI" ] || PROGRAM=nats-server; exec "$PROGRAM"`, vars: map[string]string{"CI": "1", "PROGRAM": "envoy-dispatch"}},
		{name: "trim-prefix", script: `exec envoy-${ROLE_FILE##*/}`, vars: map[string]string{"ROLE_FILE": "/opt/listener"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			actual := reviewShellRun(t, c, dir+":/bin:/usr/bin")
			fc := fakeContainer{Name: "app", Command: []string{"/bin/sh", "-c", c.script}}
			for k, v := range c.vars {
				fc.Env = append(fc.Env, env(k, v)...)
			}
			var pods podList
			if err := json.Unmarshal([]byte(podsJSON(t, fakePod{namespace: "envoy", name: "pod-assignment", containers: []fakeContainer{fc}})), &pods); err != nil {
				t.Fatal(err)
			}
			container := pods.Items[0].Spec.Containers[0]
			container.pod = &pods.Items[0]
			launch := container.launch()
			t.Logf("script=%q env=%v shell=%q named=%q writes=%q", c.script, c.vars, actual, launch.names, container.writes())
			if actual != launch.names {
				t.Errorf("actual writer %q != named writer %q", actual, launch.names)
			}
			if actual == listenerBinary {
				code, stdout, stderr := recreateIn(t, &world{podsIn: map[string]string{"envoy": podsJSON(t, fakePod{namespace: "envoy", name: "pod-assignment", containers: []fakeContainer{fc}})}}, bus.KnownSessionBucket, "envoy")
				t.Logf("recreate_code=%d stdout=%q", code, stdout)
				if code != 1 || !strings.Contains(stderr, "pod-assignment") {
					t.Error("recreate did not refuse beside the actual listener")
				}
			}
		})
	}
}

// TestReviewNestedDefaultLimit holds the fourth must-fix: a finite nest of ${R:-word} defaults, every
// R assigned empty, runs the word's writer, and readShell names it - at the depths it reads to the
// end, and, past its cap, by the reading it could not finish, never by deciding 'not a writer'.
func TestReviewNestedDefaultLimit(t *testing.T) {
	requireShell(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, listenerBinary), []byte("#!/bin/sh\nprintf '%s\\n' envoy-listener\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, depth := range []int{1, 8, 9, 12} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			word, prefix := "listener", ""
			for i := range depth {
				word = fmt.Sprintf("${R%d:-%s}", i, word)
				prefix += fmt.Sprintf("R%d=; ", i)
			}
			script := prefix + `exec "envoy-` + word + `"`
			actual := reviewShellRun(t, reviewShellCase{script: script}, dir)
			_, named := reviewNamed([]string{"-c", script}, nil)
			t.Logf("depth=%d shell=%q named=%t", depth, actual, named)
			if actual != listenerBinary {
				t.Fatalf("shell control at depth %d ran %q, not the listener", depth, actual)
			}
			if !named {
				t.Errorf("a finite nested default at depth %d was not named a writer", depth)
			}
		})
	}
}
