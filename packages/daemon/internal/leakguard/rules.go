package leakguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sync"

	"github.com/spf13/viper"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
)

// RulesSecret is the launch secret that carries the deployment's private-name rule file to every
// launch the daemon makes, and RulesFileVar the variable the guard reads the rule file's path
// from, in a role and in the daemon alike. A runtime hands each launch secret to the agent as a
// file behind `<NAME>_FILE` (internal/runtime's secretfiles.go), so the pointer a role's guard
// reads is the secret's own pointer. One is written as the other and the suffix, so the two
// cannot part.
const (
	RulesSecret  = "LEGION_PRIVATE_NAME_RULES"
	RulesFileVar = RulesSecret + "_FILE"
)

// maxRulesFile is the most of a rule file the guard reads. A rule file is a page of regular
// expressions: a deployment's runs to a few kilobytes. A mebibyte is far past any of them, so no
// rule file meets the bound, and a pointer to a log or an image by mistake is refused rather than
// read into memory whole.
const maxRulesFile = 1 << 20

// RuleSet is a compiled rule file: one detector every record of a write is scanned with. A set is
// shared by every write of a process that reads the same content (LoadRules): the detector keeps
// nothing of a scan, since Detect of a fragment answers its findings and holds none of them.
type RuleSet struct {
	detector *detect.Detector
	count    int
	sha256   string
}

// Count is the number of rules the set holds.
func (r *RuleSet) Count() int {
	return r.count
}

// SHA256 is the sha256, in hex, of the content the set was compiled from.
func (r *RuleSet) SHA256() string {
	return r.sha256
}

// loaded are the rule sets this process compiled, by the sha256 of their content. A guard reads
// the rule file at each write (Guard.rules), so a file changed under a running process is read as
// it is now; the content it holds is compiled once. Only a set that compiled is kept, so a file
// that was refused is read and refused again at the next write rather than remembered.
var loaded sync.Map

// LoadRules compiles content, a gitleaks rule file, into a rule set. It refuses what would
// otherwise lose a rule without a word:
//
//   - content past maxRulesFile;
//   - content that is no TOML;
//   - a key gitleaks' rule format does not have, named in the error: gitleaks' own read ignores an
//     unknown key, so a misspelt `[[rule]]` would compile to no rule at all;
//   - an `[extend]` section: gitleaks reads an extended file through its package-level viper and
//     a package-level depth it never resets, so a third compile in one process skips the extension,
//     and it reads `extend.path` relative to the working directory, which a daemon and a role do
//     not share;
//   - a file that holds no rule, which would run nothing while it reads as cover;
//   - and a pattern that does not compile, a panic of regexp.MustCompile inside gitleaks'
//     Translate, recovered here into an error so that a broken file refuses the write where it
//     would otherwise end the process that read it.
//
// It asks for no rule of any id: the file is the deployment's, and what it holds is the whole of
// what the guard detects.
func LoadRules(content []byte) (*RuleSet, error) {
	if len(content) > maxRulesFile {
		return nil, fmt.Errorf("the rules are longer than %d bytes", maxRulesFile)
	}
	sum := sha256.Sum256(content)
	key := hex.EncodeToString(sum[:])
	if set, ok := loaded.Load(key); ok {
		return set.(*RuleSet), nil
	}
	set, err := compileRules(content, key)
	if err != nil {
		return nil, err
	}
	kept, _ := loaded.LoadOrStore(key, set)
	return kept.(*RuleSet), nil
}

// compileRules is LoadRules' compile of content, whose sha256 is sum.
func compileRules(content []byte, sum string) (set *RuleSet, err error) {
	defer func() {
		if panicked := recover(); panicked != nil {
			set, err = nil, fmt.Errorf("the rules did not compile: %v", panicked)
		}
	}()
	// A viper of this package's own, never the package-level one: the global viper is a process's
	// single configuration, and a daemon that read its own configuration through viper would
	// share it with this.
	vp := viper.New()
	vp.SetConfigType("toml")
	if err := vp.ReadConfig(bytes.NewReader(content)); err != nil {
		return nil, fmt.Errorf("the rules are not TOML: %s", oneLine.Replace(err.Error()))
	}
	// IsSet and not the decoded Extend: an empty `[extend]` decodes to the zero Extend, and it is
	// refused all the same, since it says the file means to read another.
	if vp.IsSet("extend") {
		return nil, errors.New("the rules hold an [extend] section, which the guard does not read: write the extended rules into the file itself")
	}
	var raw config.ViperConfig
	if err := vp.UnmarshalExact(&raw); err != nil {
		return nil, fmt.Errorf("the rules are not gitleaks' rule format: %s", oneLine.Replace(err.Error()))
	}
	cfg, err := raw.Translate()
	if err != nil {
		return nil, fmt.Errorf("the rules did not compile: %w", err)
	}
	if len(cfg.Rules) == 0 {
		return nil, errors.New("the rules hold no rule")
	}
	detector := detect.NewDetector(cfg)
	// The engine's own inline escape is turned off. A `gitleaks:allow` anywhere on a line makes
	// the engine drop every finding of that line, and the script these rules are ported from,
	// .github/scripts/check-private-names.sh, honours no such thing: left on, this package would
	// hand a writer an opt-out from the classes that the check it stands in for never had, and
	// the whole of the cover would be one comment wide. The release lane's own gitleaks is turned
	// off the same way, with --ignore-gitleaks-allow (.github/scripts/release-push.sh). It
	// changes no rule and no match: the cost is that a legitimate text holding those two words
	// beside a name a rule matches is refused like any other, and its writer rewrites the name
	// rather than exempting the line.
	detector.IgnoreGitleaksAllow = true
	return &RuleSet{detector: detector, count: len(cfg.Rules), sha256: sum}, nil
}

// ReadRulesFile reads the rule file at path and compiles it (LoadRules). A file that cannot be
// opened or read, one longer than maxRulesFile, and one LoadRules refuses are each an error that
// names the file.
func ReadRulesFile(path string) (*RuleSet, error) {
	set, bad := readRulesFile(path)
	if bad != nil {
		return nil, bad
	}
	return set, nil
}

// rulesFileError is a rule file the guard cannot use: the file, why in a phrase that follows the
// file's name, and the failure beneath where there is one. It is a type so that the guard can say
// which variable named the file between the file and the why (Guard.rules).
type rulesFileError struct {
	path, why string
	err       error
}

func (e *rulesFileError) Error() string {
	message := "the private-name rule file " + e.path + " " + e.why
	if e.err != nil {
		message += ": " + e.err.Error()
	}
	return message
}

func (e *rulesFileError) Unwrap() error {
	return e.err
}

// readRulesFile is ReadRulesFile with its failure typed.
func readRulesFile(path string) (*RuleSet, *rulesFileError) {
	file, err := os.Open(path)
	if err != nil {
		return nil, &rulesFileError{path: path, why: "could not be opened", err: withoutPath(err)}
	}
	defer file.Close()
	// One byte past the bound, so a file at the bound reads whole and a longer one is known to be
	// longer without reading it.
	content, err := io.ReadAll(io.LimitReader(file, maxRulesFile+1))
	if err != nil {
		return nil, &rulesFileError{path: path, why: "could not be read", err: withoutPath(err)}
	}
	if len(content) > maxRulesFile {
		return nil, &rulesFileError{path: path, why: fmt.Sprintf("is longer than %d bytes", maxRulesFile)}
	}
	set, err := LoadRules(content)
	if err != nil {
		return nil, &rulesFileError{path: path, why: "is no rule file the guard can run", err: err}
	}
	return set, nil
}

// withoutPath is err reduced to what went wrong where it is a *fs.PathError, whose own message
// repeats the path: the message err goes into names the file already.
func withoutPath(err error) error {
	var path *fs.PathError
	if errors.As(err, &path) {
		return path.Err
	}
	return err
}

// pass is the rule pass over records, reported as a refusal of what.
func (r *RuleSet) pass(ctx context.Context, what string, records []Record) error {
	lines, err := r.scan(ctx, records)
	if err != nil {
		return &SetupFailure{What: what, Why: "the private-name rules did not finish", Err: err}
	}
	if len(lines) == 0 {
		return nil
	}
	return &Refusal{What: what, By: byRules, Lines: lines}
}

// scan is the report of records: one line per refusal, naming the record and the class, the
// description the deployment's rule gives it. The same class against the same record is one line
// however many times it matches, since the report says what a record names rather than how often,
// and a record's classes are sorted because the engine reads its rules out of a map and would
// otherwise order two of them differently from one run to the next.
//
// Each record is scanned on its own, with Detect of a fragment rather than DetectSource of a
// source: the detector accumulates the findings of a source, and a scan of several records
// through it would report the first record's finding against every record after it.
func (r *RuleSet) scan(ctx context.Context, records []Record) ([]string, error) {
	var lines []string
	for _, record := range records {
		var of []string
		seen := map[string]bool{}
		for _, finding := range r.detector.DetectContext(ctx, detect.Fragment{Raw: record.Text, FilePath: record.Label}) {
			line := record.Label + ": " + finding.Description
			if !seen[line] {
				seen[line] = true
				of = append(of, line)
			}
		}
		slices.Sort(of)
		lines = append(lines, of...)
		// A context that ended stops the detector's own scan loop and leaves it answering no
		// findings, which would read as a record that holds nothing. A bound that ran out is a
		// setup failure instead, so the write is refused rather than passed.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return lines, nil
}
