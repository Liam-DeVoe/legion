package leakguard

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/spf13/viper"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/report"
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

// The bounds of a rule pass's report (RuleSet.scan), so that a report stays a page whatever the
// write is: a body can be a mebibyte of one line and a push thousands of added lines, and the
// report is read in a terminal and in a log. A match is shown to maxMatchShown bytes; the line it
// stands on, to maxContextShown bytes on either side of the part of the match shown; and the
// report, to maxLinesShown lines and one more that counts the lines it leaves out.
const (
	maxMatchShown   = 200
	maxContextShown = 120
	maxLinesShown   = 40
)

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
//   - two rules of one id: gitleaks keeps a file's rules by id, so the second would replace the
//     first without a word;
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
	ids := make(map[string]bool, len(raw.Rules))
	for _, rule := range raw.Rules {
		if ids[rule.ID] {
			return nil, fmt.Errorf("the rules hold two rules of the id %q, and gitleaks would run the last of them alone", rule.ID)
		}
		ids[rule.ID] = true
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
	// the engine drop every finding of that line: left on, it would hand every writer an opt-out
	// from each rule of the deployment's file, one comment wide, which no rule file can take
	// back. The release lane's own gitleaks is turned off the same way, with
	// --ignore-gitleaks-allow (.github/scripts/release-push.sh). It changes no rule and no match:
	// the cost is that a legitimate text holding those two words beside a name a rule matches is
	// refused like any other, and its writer rewrites the name rather than exempting the line.
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

// scan is the report of records: one line per finding,
//
//	<record>: <class>: matched "<match>" on line <n>: "<line>"
//
// naming the record by its label as it was written, the class by the description the deployment's
// rule gives it, and then the text that matched and the line of the record it stands on. Both are
// quoted (strconv.Quote), so that a newline or a control character in them cannot break a report
// apart, and both are cut to the bounds above on a rune boundary, each cut marked "…" inside the
// quotes. <n> counts from the record's first line in its file (Record.FirstLine). A path rule's
// finding stands on no line, and is "<record>: <class>: matched the path".
//
// The report shows the text because the agent that would make the write wrote it: there is
// nothing in it to hide from the agent the refusal is shown to, and this repository's public CI
// never prints the guard's report.
//
// A line is written once however many findings write it: a path rule matches every record of a
// file, and two refs of one push can carry two commits that add one line at one place. Lines come
// in the order of the records, then of the line in a record, then of the class and of the match,
// since the engine reads its rules out of a map and would otherwise order two findings of one line
// differently from one run to the next.
//
// Each record is scanned on its own, with Detect of a fragment rather than DetectSource of a
// source: the detector accumulates the findings of a source, and a scan of several records
// through it would report the first record's finding against every record after it.
func (r *RuleSet) scan(ctx context.Context, records []Record) ([]string, error) {
	var shown shownReport
	for _, record := range records {
		findings := r.detector.DetectContext(ctx, detect.Fragment{Raw: record.Text, FilePath: record.Path})
		// A context that ended stops the detector's own scan loop and leaves it answering no
		// findings, which would read as a record that holds nothing. A bound that ran out is a
		// setup failure instead, so the write is refused rather than passed.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(findings) == 0 {
			continue
		}
		text := newRecordText(record.Text)
		places := make([]placed, len(findings))
		for i := range findings {
			places[i] = r.place(text, &findings[i])
		}
		slices.SortFunc(places, func(a, b placed) int {
			return cmp.Or(
				cmp.Compare(a.line, b.line),
				strings.Compare(a.finding.Description, b.finding.Description),
				strings.Compare(a.finding.Match, b.finding.Match),
				cmp.Compare(a.at, b.at),
			)
		})
		for _, p := range places {
			shown.add(text.reportLine(record, p))
		}
	}
	return shown.lines(), nil
}

// shownReport is a report being written: the lines it shows, a sum of every line written so far,
// so that a line is written once, and the count of the lines past maxLinesShown. It keeps a sum of
// each line rather than the line, so that a write of many thousand findings holds a sum, and not
// a line, of each it leaves out.
type shownReport struct {
	shown []string
	seen  map[[sha256.Size]byte]bool
	left  int
}

func (s *shownReport) add(line string) {
	sum := sha256.Sum256([]byte(line))
	if s.seen[sum] {
		return
	}
	if s.seen == nil {
		s.seen = map[[sha256.Size]byte]bool{}
	}
	s.seen[sum] = true
	if len(s.shown) < maxLinesShown {
		s.shown = append(s.shown, line)
		return
	}
	s.left++
}

// lines is the report: the lines shown, and where any were left out, one more that counts them.
func (s *shownReport) lines() []string {
	if s.left == 0 {
		return s.shown
	}
	return append(s.shown, fmt.Sprintf("… and %d more matches not shown", s.left))
}

// recordText is a record's text and the offset of each of its newlines, read once for a record
// with a finding to place, so that placing each of many findings costs a search and not a read of
// the text. Lines count from 0 here, as the engine counts them.
type recordText struct {
	text     string
	newlines []int
}

func newRecordText(text string) *recordText {
	t := &recordText{text: text}
	for at := 0; ; {
		i := strings.IndexByte(text[at:], '\n')
		if i < 0 {
			return t
		}
		t.newlines = append(t.newlines, at+i)
		at += i + 1
	}
}

// start is the offset line k starts at, for k from 0 to the number of newlines.
func (t *recordText) start(k int) int {
	if k == 0 {
		return 0
	}
	return t.newlines[k-1] + 1
}

// end is the offset line k ends at: its newline, or the end of the text for the last line.
func (t *recordText) end(k int) int {
	if k < len(t.newlines) {
		return t.newlines[k]
	}
	return len(t.text)
}

// lineOf is the line the byte at offset at stands on.
func (t *recordText) lineOf(at int) int {
	k, _ := slices.BinarySearch(t.newlines, at)
	return k
}

// placed is one finding of a record and where it stands: line is the line of the record its match
// starts on, and -1 for a path rule's finding, which stands on none; at is the offset its match
// starts at; and found is whether the match was found there at all (RuleSet.place).
type placed struct {
	finding  *report.Finding
	line, at int
	found    bool
}

// place is where finding stands in text. A finding of a rule with no pattern is a path rule's,
// which reads the record's Path alone.
//
// The engine says where a match starts as a line counted from 0 (StartLine) and a column counted
// from the newline before that line, so that a line's first byte is column 1 on the first line and
// column 2 on every later one (StartColumn); and it trims the newlines a match begins with off the
// match's text and not off its place. TestPushRefusalShowsMatch holds the engine to the line, and
// TestRefusalShowsMatchWithinItsBounds to the column. Where the match stands at the place that
// reading gives, that is its place, which tells apart two matches of one line. Where it does not,
// it is placed at the first place it stands on or after the start of the line the engine names,
// and where it stands nowhere, at the start of that line, with no part of the line its own. The
// engine decodes nothing for this package (its MaxDecodeDepth is 0), so each match is text of the
// record's, and that last place is one no finding of this engine is put in.
func (r *RuleSet) place(text *recordText, finding *report.Finding) placed {
	if r.detector.Config.Rules[finding.RuleID].Regex == nil {
		return placed{finding: finding, line: -1}
	}
	k := min(max(finding.StartLine, 0), len(text.newlines))
	start := text.start(k)
	at := start + finding.StartColumn - 1
	if k > 0 {
		at--
	}
	for at >= 0 && at < len(text.text) && text.text[at] == '\n' {
		at++
	}
	if at < 0 || at > len(text.text) || !strings.HasPrefix(text.text[at:], finding.Match) {
		i := strings.Index(text.text[start:], finding.Match)
		if i < 0 {
			return placed{finding: finding, line: k, at: start}
		}
		at = start + i
	}
	return placed{finding: finding, line: text.lineOf(at), at: at, found: true}
}

// reportLine is the line of record's report that p writes (RuleSet.scan). The line shown is the
// match's own line, from at most maxContextShown bytes before the match to at most
// maxContextShown bytes after the part of it shown, so that a match as long as its line shows a
// bounded line too.
func (t *recordText) reportLine(record Record, p placed) string {
	head := record.Label + ": " + p.finding.Description + ": matched "
	if p.line < 0 {
		return head + "the path"
	}
	shown := p.finding.Match
	if len(shown) > maxMatchShown {
		shown = shown[:runeBack(shown, maxMatchShown, 0)]
	}
	match := shown
	if len(shown) < len(p.finding.Match) {
		match += "…"
	}
	begin, finish := t.start(p.line), t.end(p.line)
	end := p.at
	if p.found {
		end = min(p.at+len(shown), finish)
	}
	from := runeOn(t.text, max(begin, p.at-maxContextShown), p.at)
	to := runeBack(t.text, min(finish, end+maxContextShown), end)
	line := t.text[from:to]
	if from > begin {
		line = "…" + line
	}
	if to < finish {
		line += "…"
	}
	return head + strconv.Quote(match) + " on line " + strconv.Itoa(max(record.FirstLine, 1)+p.line) + ": " + strconv.Quote(line)
}

// runeBack is the offset i of s moved back to the start of the rune it falls inside, and no
// further back than floor. It moves at most utf8.UTFMax-1 bytes, the furthest a rune's start
// stands behind one of its bytes, so that a run of bytes that is no UTF-8 is cut where it falls.
func runeBack(s string, i, floor int) int {
	for n := 1; n < utf8.UTFMax && i > floor && i < len(s) && !utf8.RuneStart(s[i]); n++ {
		i--
	}
	return i
}

// runeOn is the offset i of s moved on to the start of the next rune where it falls inside one,
// and no further on than ceiling, as runeBack moves back.
func runeOn(s string, i, ceiling int) int {
	for n := 1; n < utf8.UTFMax && i < ceiling && i < len(s) && !utf8.RuneStart(s[i]); n++ {
		i++
	}
	return i
}
