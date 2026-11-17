package specgraph

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// Scenario is a scenario of The Spec, in the format of testdata/scenarios that `spec-check state`
// reads: the approved spec, the spec files at the commit of each subject, the history of the
// repository, the tie of each check at each commit, every result, the runs of CI jobs, the
// subjects, the checks that CI refused at each commit (R48) by commit, and the work units. Units is
// nil exactly when the scenario has no units.json.
type Scenario struct {
	Approved Approved
	Files    map[string]Tree // by commit
	History  FixtureHistory
	Ties     CommitTies
	Results  []Result
	JobRuns  []JobRun
	Subjects []ScenarioSubject
	Uncited  map[string]map[string]string
	Units    []Unit
}

// ScenarioSubject is one subject of a scenario, in the form of subjects.json: the subject and the
// time that the states at it are derived at.
type ScenarioSubject struct {
	Point
	Now time.Time `json:"now"`
}

// ScenarioError is a fault of one file of a scenario: the file's path inside the scenario, the
// line of a spec file that does not parse (0 for none), and the fault.
type ScenarioError struct {
	File string
	Line int
	Err  error
}

func (e *ScenarioError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Err)
	}
	return e.File + ": " + e.Err.Error()
}

func (e *ScenarioError) Unwrap() error { return e.Err }

// The files of a scenario: each that it must hold, in the order that ReadScenario names a missing
// one, and each that it may hold.
var (
	scenarioRequired = []string{"approved.json", "files", "history.json", "jobs.json", "results.json", "subjects.json", "ties.json"}
	scenarioOptional = []string{"uncited.json", "units.json", "want.txt"}
)

// scenarioResult is one result of results.json, whose check_version names the approved version of
// the check that ran; an absent field is its zero value.
type scenarioResult struct {
	Check        string     `json:"check"`
	CheckVersion int        `json:"check_version"`
	Kind         ResultKind `json:"kind"`
	Outcome      Outcome    `json:"outcome"`
	Commit       string     `json:"commit"`
	Deploy       string     `json:"deploy"`
	GraphTx      string     `json:"graph_tx"`
	Job          string     `json:"job"`
	Leg          string     `json:"leg"`
	Attempt      int        `json:"attempt"`
	At           time.Time  `json:"at"`
	Seq          int64      `json:"seq"`
	FaultAuthor  string     `json:"fault_author"`
	TestAuthors  []string   `json:"test_authors"`
	TestCommit   string     `json:"test_commit"`
}

// The forms of the JSON files of a scenario that ReadScenario reads itself, with the type of each
// value, as decodeJSON names them for a value of another type: the form of one element of each
// array file, whose own form is "[<element>, …]", and the form of uncited.json.
const (
	subjectForm = `{"commit":"<commit>","branch":"<branch>","deploy":"<deploy>","graph_tx":"<transaction>","now":"<RFC 3339 time>"}`
	resultForm  = `{"check":"<check id>","check_version":<whole number from 1>,"kind":"<run or failing_example>","outcome":"<pass, fail or could_not_run>","commit":"<commit>","deploy":"<deploy>","graph_tx":"<transaction>","job":"<job>","leg":"<leg>","attempt":<whole number>,"at":"<RFC 3339 time>","seq":<whole number>,"fault_author":"<author>","test_authors":["<author>", …],"test_commit":"<commit>"}`
	jobRunForm  = `{"job":"<job>","leg":"<leg>","commit":"<commit>","ended":<true or false>}`
	unitForm    = `{"id":"<unit>","serves":["<statement id>", …],"needs":["<unit>", …],"waits_on":["<statement id>", …]}`
	uncitedForm = `{"<commit>":{"<check id>":"<R48 rule>"}}`
)

// ReadScenario reads the scenario at the root of fsys, and refuses any file or field that the
// format does not hold, with a *ScenarioError that names the file. Each JSON file is read by
// decodeJSON, which refuses a byte that is not UTF-8, a member that the format does not name in
// its own letters, a member that one object gives two times, text after the value, and a value of
// another type than the file's form needs, which it names with the form and the place of the value:
//
//   - approved.json, as ReadApproved reads it, and with no cycle of parent and support relations
//     among the current statements (R5: "Dispatch refuses a change that makes one"). The rules of
//     the files are not run on it: the person approves a level before the checks of its
//     statements exist, so an approved leaf can have no check, and the files then do not hold it;
//   - subjects.json, [{"commit","branch","deploy","graph_tx","now"}]: at least one subject, each
//     with a commit, a now, and exactly one of branch and deploy; graph_tx, the transaction of the
//     graph that the subject reads, only with a deploy. Each of commit, branch, deploy and graph_tx
//     that a subject sets has a letter or a digit (hasWord), since one with none names nothing.
//     A check of production or of the graph is judged only at a deploy, and a check of the graph
//     reads unknown there when graph_tx is absent or names a transaction that no result read;
//   - files/<commit>/: the spec files at the commit of each subject, each a *.md file directly in
//     it, which Parse reads and Validate judges with no problem, as `spec-check tree` does, and no
//     directory that no subject reads;
//   - history.json, as ReadFixtureHistory reads it;
//   - ties.json, {"<commit>":{"<check id>":["<path or pattern>", …]}}, as ReadCommitTies reads it:
//     a check that the object of a commit does not name has a tie that is not known at that commit,
//     and so has each check at a commit that the file does not name. A result at one commit counts
//     at another only when the check's tie is known at both commits and no path that changed
//     between them is covered by the tie at either commit;
//   - results.json, [{"check","check_version","kind","outcome","commit","deploy","graph_tx","job",
//     "leg","attempt","at","seq","fault_author","test_authors","test_commit"}]: check_version
//     names an approved version of the check, whose Digest the result carries; kind is run or
//     failing_example, and outcome pass, fail or could_not_run; a result names at most one of the
//     subjects commit, deploy and graph_tx, the one that it read;
//   - jobs.json, [{"job","leg","commit","ended"}];
//   - uncited.json (optional), {"<commit>":{"<check id>":"<R48 rule>"}};
//   - units.json (optional), [{"id","serves","needs","waits_on"}]: each id once, serves and
//     waits_on naming current statements of the approved spec, needs naming units of the file;
//   - want.txt (optional), which ReadScenario does not read.
func ReadScenario(fsys fs.FS) (Scenario, error) {
	var s Scenario
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return s, &ScenarioError{File: ".", Err: err}
	}
	holds := map[string]bool{}
	for _, entry := range entries {
		if !slices.Contains(scenarioRequired, entry.Name()) && !slices.Contains(scenarioOptional, entry.Name()) {
			return s, scenarioFault(entry.Name(), "the format of a scenario holds no such file")
		}
		holds[entry.Name()] = true
	}
	for _, name := range scenarioRequired {
		if !holds[name] {
			return s, scenarioFault(name, "does not exist")
		}
	}

	if s.Approved, err = readWith(fsys, "approved.json", ReadApproved); err != nil {
		return s, err
	}
	if cycle := approvedLoop(s.Approved); cycle != nil {
		return s, scenarioFault("approved.json", "loop: %s", loopDetail(cycle))
	}
	if s.Subjects, err = readSubjects(fsys); err != nil {
		return s, err
	}
	if s.Files, err = readScenarioFiles(fsys, s.Subjects); err != nil {
		return s, err
	}
	if s.History, err = readWith(fsys, "history.json", ReadFixtureHistory); err != nil {
		return s, err
	}
	if s.Ties, err = readWith(fsys, "ties.json", ReadCommitTies); err != nil {
		return s, err
	}
	if s.Results, err = readResults(fsys, s.Approved); err != nil {
		return s, err
	}
	if s.JobRuns, err = readEach[JobRun](fsys, "jobs.json", "job run", jobRunForm); err != nil {
		return s, err
	}
	if holds["uncited.json"] {
		if err := readJSON(fsys, "uncited.json", &s.Uncited, uncitedForm); err != nil {
			return s, err
		}
	}
	if holds["units.json"] {
		if s.Units, err = readUnits(fsys, s.Approved); err != nil {
			return s, err
		}
	}
	return s, nil
}

// Input is the input of the derivation at subject i of the scenario.
func (s Scenario) Input(i int) Input {
	subject := s.Subjects[i]
	return Input{Files: s.Files[subject.Commit], Approved: s.Approved, Results: s.Results, JobRuns: s.JobRuns,
		Ties: s.Ties, History: s.History, Subject: subject.Point, Now: subject.Now, Uncited: s.Uncited[subject.Commit]}
}

// readSubjects reads subjects.json.
func readSubjects(fsys fs.FS) ([]ScenarioSubject, error) {
	subjects, err := readEach[ScenarioSubject](fsys, "subjects.json", "subject", subjectForm)
	if err != nil {
		return nil, err
	}
	if len(subjects) == 0 {
		return nil, scenarioFault("subjects.json", "it holds no subject")
	}
	for i, sub := range subjects {
		fault := ""
		switch {
		case sub.Commit == "":
			fault = "it sets no commit"
		case !hasWord(sub.Commit):
			fault = "it sets a commit of no letter and no digit, which names no commit"
		case sub.Now.IsZero():
			fault = "it sets no now"
		case sub.Branch != "" && sub.Deploy != "":
			fault = "it sets both branch and deploy; a subject sets exactly one of them"
		case sub.Branch == "" && sub.Deploy == "":
			fault = "it sets neither branch nor deploy; a subject sets exactly one of them"
		case sub.GraphTx != "" && sub.Deploy == "":
			fault = "it sets graph_tx with no deploy; the commit that production runs reads the graph, and the head of a branch does not"
		case sub.Branch != "" && !hasWord(sub.Branch):
			fault = "it sets a branch of no letter and no digit, which names no branch"
		case sub.Deploy != "" && !hasWord(sub.Deploy):
			fault = "it sets a deploy of no letter and no digit, which names no deploy"
		case sub.GraphTx != "" && !hasWord(sub.GraphTx):
			fault = "it sets a graph_tx of no letter and no digit, which names no transaction of the graph"
		}
		if fault != "" {
			return nil, scenarioFault("subjects.json", "subject %d: %s", i+1, fault)
		}
	}
	return subjects, nil
}

// readScenarioFiles reads the spec files at the commit of each subject, files/<commit>/*.md,
// parses them and judges the tree by Validate, and refuses the first problem of Parse, or else of
// Validate, with its file and line.
func readScenarioFiles(fsys fs.FS, subjects []ScenarioSubject) (map[string]Tree, error) {
	trees := map[string]Tree{}
	for i, sub := range subjects {
		if _, read := trees[sub.Commit]; read {
			continue
		}
		dir := path.Join("files", sub.Commit)
		if !fs.ValidPath(dir) || strings.Count(dir, "/") != 1 {
			return nil, scenarioFault("subjects.json", "subject %d: commit %q is no name of a directory", i+1, sub.Commit)
		}
		entries, err := fs.ReadDir(fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, scenarioFault(dir, "does not exist, and subject %d reads the spec files of its commit there", i+1)
		}
		if err != nil {
			return nil, &ScenarioError{File: dir, Err: err}
		}
		files := map[string][]byte{}
		for _, entry := range entries {
			name := path.Join(dir, entry.Name())
			if !entry.Type().IsRegular() || path.Ext(entry.Name()) != ".md" {
				return nil, scenarioFault(name, "the spec files of a commit are *.md files directly in its directory")
			}
			if files[entry.Name()], err = fs.ReadFile(fsys, name); err != nil {
				return nil, &ScenarioError{File: name, Err: err}
			}
		}
		if len(files) == 0 {
			return nil, scenarioFault(dir, "holds no spec file")
		}
		tree, problems := Parse(files)
		if len(problems) == 0 {
			problems = Validate(tree, Options{})
		}
		if len(problems) > 0 {
			SortProblems(problems)
			p := problems[0]
			return nil, &ScenarioError{File: path.Join(dir, p.Pos.File), Line: p.Pos.Line, Err: fmt.Errorf("%s: %s", p.Node, p.Detail)}
		}
		trees[sub.Commit] = tree
	}
	entries, err := fs.ReadDir(fsys, "files")
	if err != nil {
		return nil, &ScenarioError{File: "files", Err: err}
	}
	for _, entry := range entries {
		if _, read := trees[entry.Name()]; !read {
			return nil, scenarioFault(path.Join("files", entry.Name()), "no subject reads the spec files of this commit")
		}
	}
	return trees, nil
}

// approvedLoop is a cycle of parent and serves relations among the current statements of a, as
// Validate's loop rule writes it, or nil when there is none.
func approvedLoop(a Approved) []string {
	current, _ := a.Current() // ReadApproved refused every block that would be a problem here
	if loops := findLoops(statementEdges(current.Statements)); len(loops) > 0 {
		return loops[0]
	}
	return nil
}

// readWith reads the file name with read, and names the file in its fault.
func readWith[T any](fsys fs.FS, name string, read func(io.Reader) (T, error)) (T, error) {
	f, err := fsys.Open(name)
	if err != nil {
		var zero T
		return zero, &ScenarioError{File: name, Err: err}
	}
	defer f.Close()
	v, err := read(f)
	if err != nil {
		return v, &ScenarioError{File: name, Err: err}
	}
	return v, nil
}

// readResults reads results.json, each result carrying the Digest of the approved version of its
// check that check_version names.
func readResults(fsys fs.FS, approved Approved) ([]Result, error) {
	read, err := readEach[scenarioResult](fsys, "results.json", "result", resultForm)
	if err != nil {
		return nil, err
	}
	results := make([]Result, len(read))
	for i, r := range read {
		fault, digest, held := "", "", false
		switch {
		case r.Kind != ResultRun && r.Kind != ResultFailingExample:
			fault = fmt.Sprintf("kind %q is not run or failing_example", r.Kind)
		case r.Outcome != OutcomePass && r.Outcome != OutcomeFail && r.Outcome != OutcomeCouldNotRun:
			fault = fmt.Sprintf("outcome %q is not pass, fail or could_not_run", r.Outcome)
		case len(namedSubjects(r)) > 1:
			fault = fmt.Sprintf("it names the subjects %s; a result reads one subject", strings.Join(namedSubjects(r), " and "))
		default:
			if digest, held = checkVersionDigest(approved, r.Check, r.CheckVersion); !held {
				fault = fmt.Sprintf("the approved spec holds no version %d of check %q", r.CheckVersion, r.Check)
			}
		}
		if fault != "" {
			return nil, scenarioFault("results.json", "result %d: %s", i+1, fault)
		}
		results[i] = Result{Check: r.Check, CheckDigest: digest, Kind: r.Kind, Outcome: r.Outcome, Commit: r.Commit,
			Deploy: r.Deploy, GraphTx: r.GraphTx, Job: r.Job, Leg: r.Leg, Attempt: r.Attempt, At: r.At, Seq: r.Seq,
			FaultAuthor: r.FaultAuthor, TestAuthors: r.TestAuthors, TestCommit: r.TestCommit}
	}
	return results, nil
}

// namedSubjects is the name of each subject that the result r names, of commit, deploy and
// graph_tx, in that order.
func namedSubjects(r scenarioResult) []string {
	var named []string
	for _, s := range []struct{ name, value string }{{"commit", r.Commit}, {"deploy", r.Deploy}, {"graph_tx", r.GraphTx}} {
		if s.value != "" {
			named = append(named, s.name)
		}
	}
	return named
}

// checkVersionDigest is the Digest of version v of the check id in the approved spec, and whether
// the approved spec holds that version of a check.
func checkVersionDigest(approved Approved, id string, v int) (string, bool) {
	i := slices.IndexFunc(approved.Nodes[id].Versions, func(av ApprovedVersion) bool { return av.Version == v })
	if i < 0 {
		return "", false
	}
	text, problems := versionText(id, approved.Nodes[id].Versions[i])
	if len(problems) > 0 || len(text.Checks) != 1 {
		return "", false
	}
	return text.Checks[0].Digest(), true
}

// readUnits reads units.json.
func readUnits(fsys fs.FS, approved Approved) ([]Unit, error) {
	units, err := readEach[Unit](fsys, "units.json", "unit", unitForm)
	if err != nil {
		return nil, err
	}
	current, _ := approved.Current()
	statements := make(map[string]bool, len(current.Statements))
	for _, st := range current.Statements {
		statements[st.ID] = true
	}
	ids := map[string]bool{}
	for i, u := range units {
		if u.ID == "" {
			return nil, scenarioFault("units.json", "unit %d has no id", i+1)
		}
		if ids[u.ID] {
			return nil, scenarioFault("units.json", "unit %q is in the file two times", u.ID)
		}
		ids[u.ID] = true
	}
	for _, u := range units {
		for _, field := range []struct {
			name string
			ids  []string
		}{{"serves", u.Serves}, {"waits_on", u.WaitsOn}} {
			for _, id := range field.ids {
				if !statements[id] {
					return nil, scenarioFault("units.json", "unit %q: %s names %q, which is no current statement of the approved spec", u.ID, field.name, id)
				}
			}
		}
		for _, id := range u.Needs {
			if !ids[id] {
				return nil, scenarioFault("units.json", "unit %q: needs names %q, which is no unit of the file", u.ID, id)
			}
		}
	}
	return units, nil
}

// readEach reads the JSON array of the file name, each element a T in the form form as decodeJSON
// reads it, so the file is in the form "[<form>, …]"; a fault of an element names it as
// "<noun> <n>", from 1, and a place in it from the element.
func readEach[T any](fsys fs.FS, name, noun, form string) ([]T, error) {
	var raw []jsontext.Value
	if err := readJSON(fsys, name, &raw, "["+form+", …]"); err != nil {
		return nil, err
	}
	all := make([]T, len(raw))
	for i, element := range raw {
		if err := decodeJSON(element, &all[i], form); err != nil {
			return nil, scenarioFault(name, "%s %d: %w", noun, i+1, err)
		}
	}
	return all, nil
}

// readJSON decodes the file name into v with decodeJSON, in the form form.
func readJSON(fsys fs.FS, name string, v any, form string) error {
	content, err := fs.ReadFile(fsys, name)
	if err != nil {
		return &ScenarioError{File: name, Err: err}
	}
	if err := decodeJSON(content, v, form); err != nil {
		return &ScenarioError{File: name, Err: err}
	}
	return nil
}

// scenarioFault is the fault of the file name, the format filled with args.
func scenarioFault(name, format string, args ...any) *ScenarioError {
	return &ScenarioError{File: name, Err: fmt.Errorf(format, args...)}
}
