package harness

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Exposure is one line of the exposure log (INFRA-3189's, R3 here): one run that read the
// held-out part.
type Exposure struct {
	// Commit is the commit the run ran at, and Time when it started (RFC 3339, UTC).
	Commit string `json:"commit"`
	Time   string `json:"time"`
	// Changes are the changes the run scored, and Models the models it called by role.
	Changes []string          `json:"changes"`
	Models  map[string]string `json:"models"`
	// Reason is the --new-run reason of a second run at one commit, empty for a first run.
	Reason string `json:"reason,omitempty"`
	// Out is the run's output directory.
	Out string `json:"out"`
}

// DefaultExposureLog is the exposure log's path: $TMPDIR/infra-3231-impl/harness/exposure.log.
func DefaultExposureLog() string {
	return filepath.Join(os.Getenv("TMPDIR"), "infra-3231-impl", "harness", "exposure.log")
}

// ReadExposures reads the exposure log at path; a log that does not exist holds no run.
func ReadExposures(path string) ([]Exposure, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Exposure
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Exposure
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}

// ClaimHeldOut appends e to the exposure log at path before a run reads the held-out part. It
// refuses when the log already holds a run at e's commit and e gives no reason for a new one,
// and when e names no commit.
func ClaimHeldOut(path string, e Exposure) error {
	if strings.TrimSpace(e.Commit) == "" {
		return errors.New("a run that reads the held-out part names its commit (--commit)")
	}
	logged, err := ReadExposures(path)
	if err != nil {
		return err
	}
	for _, prior := range logged {
		if prior.Commit == e.Commit && strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("the exposure log %s already holds a run of the held-out part at commit %s (%s, changes %s): give --new-run '<reason>' to run it again",
				path, prior.Commit, prior.Time, strings.Join(prior.Changes, ","))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// headCommit is the commit git names HEAD in the working directory: in a jj workspace colocated
// with git, the parent of the working-copy commit, which is the named commit of a held-out run
// made on a clean working copy.
func headCommit() (string, error) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// outsideRepository refuses an output path inside a repository: a directory at or above it that
// holds a .git or a .jj. Every file the harness writes holds Dispatch or session text, which
// goes under $TMPDIR and never into a working copy.
func outsideRepository(flag, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for d := abs; ; d = filepath.Dir(d) {
		for _, marker := range []string{".git", ".jj"} {
			if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
				return fmt.Errorf("%s %s is inside the repository at %s: its files hold Dispatch text, so give a path under $TMPDIR", flag, path, d)
			}
		}
		if filepath.Dir(d) == d {
			return nil
		}
	}
}
