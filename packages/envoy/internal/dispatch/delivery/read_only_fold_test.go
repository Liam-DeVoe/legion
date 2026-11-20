package delivery

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// resultsHeadsRead is one entry of resultsHeadsReads: one read of pull_request_heads on the
// results path, which plan D30 makes one statement for both of its callers (headsStatement). A
// writer's results read runs it over the stored rows alone, and a read-only caller's over the
// stored rows with the heads the read-only memory keeps laid on (KeptHeads, keptHeadsRelation).
type resultsHeadsRead struct {
	// read is the read as plan D30 names it: the name of its statement where the statement has
	// one (headsAttributedQuery), and else the name of the writer's function (HeadStoredAt).
	read string
	// writer is the function a writer's results read calls for the read.
	writer string
	// statement is the name of the package-level constant or variable that holds the read's one
	// statement text: the text after its WITH list, which writer and every entry point of
	// readOnly run. Each of them must reach it through the package's own functions and values.
	// It is "" only while readOnly is empty.
	statement string
	// readOnly is the read's read-only entry points: each function or method of the package that
	// takes the memory's answers (memoryAnswerTypes) and runs statement over them for a read-only
	// caller. A function is named by its name, and a method as Type.Method.
	readOnly []string
}

// resultsHeadsReads is the registry of the reads of pull_request_heads on the results path: the
// five reads of plan D30. An entry is one read, with the writer's function, the read's one
// statement after its WITH list, and its read-only entry point, which takes what the memory gives
// (CommitPullAnswers.Learned) and runs that statement's read-only form. A function that takes the
// memory's answers to read these rows for a read-only caller is named here, among the read-only
// entry points of the read whose statement it runs, and nowhere else.
// TestEachHeadsReadOnTheResultsPathIsOneStatement fails on every function of the package that
// takes the memory's answers, or whose name ends in With, and that neither this registry nor
// keptBranchReads names: such a function is a second copy of a writer's read, which no test holds
// to the writer's statement. TestTheReadOnlyFoldAnswersAsTheWritersPath asks each read of the
// registry its questions (foldQuestions).
var resultsHeadsReads = []resultsHeadsRead{
	{read: "headsAttributedQuery", writer: "HeadsAttributedTo", statement: "headsAttributedQuery",
		readOnly: []string{"ReadOnlyHeadsAttributedTo"}},
	{read: "HeadStoredAt", writer: "HeadStoredAt", statement: "headStoredAtQuery",
		readOnly: []string{"ReadOnlyHeadStoredAt"}},
	{read: "BaseBranchMergedFor", writer: "BaseBranchMergedFor", statement: "baseBranchMergedForQuery",
		readOnly: []string{"ReadOnlyBaseBranchMergedFor"}},
	{read: "baseBranchMergedInQuery", writer: "BaseBranchMergedIn", statement: "baseBranchMergedInQuery",
		readOnly: []string{"ReadOnlyBaseBranchMergedIn"}},
	{read: "NewestPullRequestHead", writer: "NewestPullRequestHead", statement: "newestPullRequestHeadQuery",
		readOnly: []string{"ReadOnlyNewestPullRequestHead"}},
}

// keptBranchReads is the functions and methods that may take the memory's answers and read no
// pull_request_heads row: the branch reads plan D5 keeps, which read a merge the memory learned as
// the yes a writer's store of it would have written (Branches.ReadCommitOnBranch). Each is named
// as resultsHeadsRead.readOnly names an entry point.
var keptBranchReads = []string{
	"Branches.ReadCommitOnBranch",
	"Branches.readCommitOnBranch",
	"mergeLearnedOnBranch",
}

// memoryAnswerTypes is each type a parameter carries the memory's answers in, written as Go
// writes the type: KeptHeads, the heads CommitPullAnswers.Learned gives, which every read-only
// entry point takes; and []CommitPulls, the answers as the read-throughs found them, which the Go
// copies of the reads this test replaced took. A pointer is read as the type it points to, and a
// variadic parameter as the slice it is (answerType).
var memoryAnswerTypes = []string{"KeptHeads", "[]CommitPulls"}

// TestEachHeadsReadOnTheResultsPathIsOneStatement is plan D30's inventory of the reads of
// pull_request_heads on the results path. It parses the package's non-test files, as go build
// selects them, and fails on three things, naming each:
//
//   - a function or method that takes the memory's answers (memoryAnswerTypes), or whose name
//     ends in With, and that the registry does not name (resultsHeadsReads, keptBranchReads);
//   - a name in the registry that the package does not declare, so the registry keeps no place
//     for a copy that has gone;
//   - a read with a read-only entry point whose statement is not a constant or variable of the
//     package, or is not reached from the read's writer and from each of its entry points.
func TestEachHeadsReadOnTheResultsPathIsOneStatement(t *testing.T) {
	source := parsePackageSource(t)

	registered := map[string]bool{}
	for _, name := range keptBranchReads {
		registered[name] = true
	}
	for _, read := range resultsHeadsReads {
		for _, name := range read.readOnly {
			registered[name] = true
		}
	}
	for _, function := range source.functions {
		if registered[function.key] {
			continue
		}
		var why []string
		if takes := function.memoryAnswers(); takes != "" {
			why = append(why, "takes the memory's answers as "+takes)
		}
		if strings.HasSuffix(function.decl.Name.Name, "With") {
			why = append(why, "has a name that ends in With")
		}
		if len(why) == 0 {
			continue
		}
		t.Errorf("%s: %s %s, and the registry names it nowhere (resultsHeadsReads, keptBranchReads): "+
			"it is a second copy of a writer's read, which no test holds to the writer's statement. "+
			"Run the read's one statement instead, and name the function among that read's read-only entry points",
			source.positions.Position(function.decl.Pos()), function.key, strings.Join(why, " and "))
	}

	for _, name := range keptBranchReads {
		if !source.declaresFunction(name) {
			t.Errorf("keptBranchReads names %s, which is no function or method of the package", name)
		}
	}
	for _, read := range resultsHeadsReads {
		if !source.declaresFunction(read.read) && !source.values[read.read] {
			t.Errorf("resultsHeadsReads names the read %s, which the package does not declare", read.read)
		}
		if !source.declaresFunction(read.writer) {
			t.Errorf("the read %s names its writer %s, which is no function of the package", read.read, read.writer)
		}
		for _, name := range read.readOnly {
			if !source.declaresFunction(name) {
				t.Errorf("the read %s names the read-only entry point %s, which is no function or method of the package", read.read, name)
			}
		}
		if len(read.readOnly) == 0 {
			continue
		}
		if !source.values[read.statement] {
			t.Errorf("the read %s has read-only entry points and names its statement %q, which is no constant or variable of the package",
				read.read, read.statement)
			continue
		}
		for _, caller := range append([]string{read.writer}, read.readOnly...) {
			if source.declaresFunction(caller) && !source.reaches(caller, read.statement) {
				t.Errorf("%s does not run %s, the one statement of the read %s", caller, read.statement, read.read)
			}
		}
	}
}

// packageSource is the package's non-test files as TestEachHeadsReadOnTheResultsPathIsOneStatement
// reads them: every function and method in source order, every package-level constant and
// variable, and the names each of them refers to.
type packageSource struct {
	positions *token.FileSet
	functions []packageFunction
	// values is every package-level constant and variable, by name.
	values map[string]bool
	// refers is the names each function, method, constant and variable refers to, by its key.
	refers map[string]map[string]bool
	// declared is the keys a name may refer to: a function, a constant or a variable of that
	// name, and every method of that name.
	declared map[string][]string
}

// packageFunction is one function or method of the package, with its key: its name, or
// Type.Method for a method.
type packageFunction struct {
	key  string
	decl *ast.FuncDecl
}

// parsePackageSource parses the non-test files go build selects for the package in the test's
// own directory.
func parsePackageSource(t *testing.T) *packageSource {
	t.Helper()
	listed, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("list the package's files: %v", err)
	}
	source := &packageSource{
		positions: token.NewFileSet(),
		values:    map[string]bool{},
		refers:    map[string]map[string]bool{},
		declared:  map[string][]string{},
	}
	for _, name := range listed.GoFiles {
		file, err := parser.ParseFile(source.positions, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				key := decl.Name.Name
				if decl.Recv != nil && len(decl.Recv.List) > 0 {
					key = receiverName(decl.Recv.List[0].Type) + "." + decl.Name.Name
				}
				source.functions = append(source.functions, packageFunction{key: key, decl: decl})
				source.declare(decl.Name.Name, key, decl.Body)
			case *ast.GenDecl:
				if decl.Tok != token.CONST && decl.Tok != token.VAR {
					continue
				}
				for _, spec := range decl.Specs {
					value := spec.(*ast.ValueSpec)
					for _, name := range value.Names {
						if name.Name == "_" {
							continue
						}
						source.values[name.Name] = true
						source.declare(name.Name, name.Name, value)
					}
				}
			}
		}
	}
	return source
}

// declare records key, which name refers to, and the names node refers to as key's.
func (s *packageSource) declare(name, key string, node ast.Node) {
	s.declared[name] = append(s.declared[name], key)
	refers := map[string]bool{}
	if node != nil {
		ast.Inspect(node, func(node ast.Node) bool {
			if ident, isIdent := node.(*ast.Ident); isIdent {
				refers[ident.Name] = true
			}
			return true
		})
	}
	s.refers[key] = refers
}

// declaresFunction is whether key is a function or method of the package.
func (s *packageSource) declaresFunction(key string) bool {
	for _, function := range s.functions {
		if function.key == key {
			return true
		}
	}
	return false
}

// reaches is whether from refers to target, directly or through the package's functions,
// methods, constants and variables. A name is read as every declaration of the package it could
// refer to, so a method call reaches every method of that name: the test asks whether the
// statement is run at all, not by which path.
func (s *packageSource) reaches(from, target string) bool {
	seen := map[string]bool{from: true}
	next := []string{from}
	for len(next) > 0 {
		key := next[0]
		next = next[1:]
		for name := range s.refers[key] {
			for _, referred := range s.declared[name] {
				if referred == target {
					return true
				}
				if !seen[referred] {
					seen[referred] = true
					next = append(next, referred)
				}
			}
		}
	}
	return false
}

// memoryAnswers is the type in which a parameter of the function carries the memory's answers
// (memoryAnswerTypes), and "" where no parameter does.
func (f packageFunction) memoryAnswers() string {
	for _, field := range f.decl.Type.Params.List {
		written := answerType(field.Type)
		for _, answers := range memoryAnswerTypes {
			if written == answers {
				return written
			}
		}
	}
	return ""
}

// answerType is a parameter's type as memoryAnswerTypes writes one: as Go writes it, with every
// pointer read as the type it points to, and a variadic parameter's as the slice it is.
func answerType(expr ast.Expr) string {
	if variadic, isVariadic := expr.(*ast.Ellipsis); isVariadic {
		return "[]" + answerType(variadic.Elt)
	}
	return strings.ReplaceAll(types.ExprString(expr), "*", "")
}

// receiverName is the name of a method's receiver type, without its pointer or its type
// parameters.
func receiverName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.StarExpr:
		return receiverName(expr.X)
	case *ast.IndexExpr:
		return receiverName(expr.X)
	case *ast.IndexListExpr:
		return receiverName(expr.X)
	case *ast.Ident:
		return expr.Name
	}
	return types.ExprString(expr)
}

// foldIssue is the issue of deliveryRepo the fold histories attribute pull requests to, and
// foldLinkingIssue an issue of gadgetsRepo that links deliveryRepo's pull request 5, so its
// linked arm reads that pull request's row in every history (linkedPullRequestTerms).
const (
	foldIssue        = "HEAD-701"
	foldLinkingIssue = "GDGT-702"
)

// foldWriter names the writer of one step of a fold history.
type foldWriter string

const (
	// foldDelivery is a `pull_request` envelope's write (UpsertPullRequestHead).
	foldDelivery foldWriter = "a delivery"
	// foldReadThrough is a writer's read-through (StoreCommitPulls).
	foldReadThrough foldWriter = "a writer's read-through"
	// foldLearned is no writer: an answer a read-only read learned and kept in its memory.
	foldLearned foldWriter = "an answer learned"
)

// foldStep is one step of a fold history, in time order.
type foldStep struct {
	writer foldWriter
	pulls  CommitPulls
	// changes is whether a writer's write changes its pull request's row: it inserts it, or its
	// event-time guard holds. The test fails where PostgreSQL did otherwise.
	changes bool
	// sha and at are the commit an answer was learned for and the moment it was learned.
	sha string
	at  time.Time
}

// foldQuestion is one question asked of one read of resultsHeadsReads: the read's name there and
// its arguments.
type foldQuestion struct {
	read string
	args []string
}

func (q foldQuestion) String() string {
	return q.read + "(" + strings.Join(q.args, ", ") + ")"
}

// TestTheReadOnlyFoldAnswersAsTheWritersPath holds the read-only form of each read of the results
// path (resultsHeadsReads) to the writer's own path, history by history (plan D30). A history is
// a sequence of writes in time order: the writers' writes, each run through the writer's own
// statement on the history's database, and the answers a read-only read learned, each kept in its
// memory the way the production code keeps one (foldLearn). The oracle applies D30's rule with the
// writer's own statements (foldOracle): where no writer's write changed a pull request's row after
// an answer for it was learned, the answer is stored as a writer's read would have stored it then,
// the whole history run through upsertHeadRow in a transaction rolled back once the writer's read
// has run in it; where one did, the answer gives way, and where no answer stands the writer's read
// runs on the writers' rows. The read-only form runs on the rows the writers' writes alone left,
// with the memory's heads, and must give the oracle's answer to every question (foldQuestions).
//
// Whether a writer's write changed a row is what PostgreSQL did, read from the row's xmin before
// and after the write (foldRowVersions), and each writer step says what it means to do
// (foldStep.changes), so a history meant to leave its answer standing fails where it does not.
func TestTheReadOnlyFoldAnswersAsTheWritersPath(t *testing.T) {
	at := func(minute int) time.Time { return time.Date(2026, 11, 20, 9, minute, 0, 0, time.UTC) }
	read := func(minute int) *time.Time { moment := at(minute); return &moment }
	sha := func(digit string) string { return strings.Repeat(digit, 40) }
	// The heads a history writes or learns: deliveryRepo's pull request number at a commit, open,
	// credited to nobody by a read of every source and carrying no time, then changed by the
	// functions after it.
	head := func(number int, commit string) PullRequestHead {
		return foldHead(deliveryRepo, number, commit)
	}
	credited := func(head PullRequestHead, issue string) PullRequestHead {
		head.IssueKey, head.AttributionSource = &issue, "title"
		return head
	}
	incomplete := func(head PullRequestHead) PullRequestHead {
		head.AttributionComplete = false
		return head
	}
	timed := func(head PullRequestHead, event time.Time, readAt *time.Time) PullRequestHead {
		head.EventAt, head.CheckedAt = &event, readAt
		return head
	}
	merged := func(head PullRequestHead, base, commit string) PullRequestHead {
		head.State, head.BaseRef, head.MergeCommitSHA = "merged", base, commit
		return head
	}
	delivered := func(changes bool, head PullRequestHead) foldStep {
		return foldStep{writer: foldDelivery, changes: changes,
			pulls: CommitPulls{Heads: []PullRequestHead{head}, storedUnder: head.Repo}}
	}
	readThrough := func(changes bool, head PullRequestHead) foldStep {
		return foldStep{writer: foldReadThrough, changes: changes,
			pulls: CommitPulls{Heads: []PullRequestHead{head}, storedUnder: head.Repo}}
	}
	learnedUnder := func(under, commit string, when time.Time, head PullRequestHead) foldStep {
		return foldStep{writer: foldLearned, sha: commit, at: when,
			pulls: CommitPulls{Heads: []PullRequestHead{head}, AtHead: head.HeadSHA == commit, storedUnder: under}}
	}
	learned := func(commit string, when time.Time, head PullRequestHead) foldStep {
		return learnedUnder(head.Repo, commit, when, head)
	}

	for _, history := range []struct {
		name  string
		steps []foldStep
	}{
		{"a writer's row and nothing learned", []foldStep{
			delivered(true, timed(credited(head(5, sha("1")), foldIssue), at(1), read(10))),
		}},
		{"an answer learned and no row", []foldStep{
			learned(sha("1"), at(20), timed(credited(head(5, sha("1")), foldIssue), at(1), read(20))),
		}},
		// The row credits nobody; the answer is the merge, credited to the issue.
		{"an answer learned, then nothing written", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(1), read(10))),
			learned(sha("1"), at(20), timed(credited(merged(head(5, sha("1")), "release", sha("m")), foldIssue), at(2), read(20))),
		}},
		// The same event, read again later.
		{"an answer learned after a writer's read-through row", []foldStep{
			readThrough(true, timed(credited(head(5, sha("1")), foldIssue), at(1), read(10))),
			learned(sha("1"), at(20), timed(credited(head(5, sha("1")), foldIssue), at(1), read(20))),
		}},
		// The read-through reads the same event later and credits nobody, as a re-judge that
		// keeps updated_at does: its write moves the row's xmin.
		{"a writer's read-through after an answer learned", []foldStep{
			delivered(true, timed(credited(head(5, sha("1")), foldIssue), at(1), read(10))),
			learned(sha("1"), at(20), timed(credited(head(5, sha("1")), foldIssue), at(1), read(20))),
			readThrough(true, timed(head(5, sha("1")), at(1), read(30))),
		}},
		{"a complete answer learned, then an incomplete delivery with a newer event", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(1), read(10))),
			learned(sha("1"), at(20), timed(credited(head(5, sha("1")), foldIssue), at(2), read(20))),
			delivered(true, timed(incomplete(head(5, sha("2"))), at(3), nil)),
		}},
		{"a complete answer learned, then an incomplete read-through with a newer event", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(1), read(10))),
			learned(sha("1"), at(20), timed(credited(head(5, sha("1")), foldIssue), at(2), read(20))),
			readThrough(true, timed(incomplete(head(5, sha("2"))), at(3), nil)),
		}},
		// The delivery's event is older than the row's, so its guard fails, the row's xmin stays
		// and the answer stands.
		{"a delivery delivered after an answer learned whose event is older than the answer's, complete", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(2), read(10))),
			learned(sha("2"), at(20), timed(credited(head(5, sha("2")), foldIssue), at(3), read(20))),
			delivered(false, timed(head(5, sha("0")), at(1), nil)),
		}},
		{"a delivery delivered after an answer learned whose event is older than the answer's, incomplete", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(2), read(10))),
			learned(sha("2"), at(20), timed(credited(head(5, sha("2")), foldIssue), at(3), read(20))),
			delivered(false, timed(incomplete(head(5, sha("0"))), at(1), nil)),
		}},
		// The delivery's event is the answer's and newer than the row's, so its guard holds and
		// the row is the writer's.
		{"a late delivery whose event equals the answer's, complete", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(1), read(10))),
			learned(sha("2"), at(20), timed(credited(head(5, sha("2")), foldIssue), at(2), read(20))),
			delivered(true, timed(head(5, sha("2")), at(2), nil)),
		}},
		{"a late delivery whose event equals the answer's, incomplete", []foldStep{
			delivered(true, timed(head(5, sha("1")), at(1), read(10))),
			learned(sha("2"), at(20), timed(credited(head(5, sha("2")), foldIssue), at(2), read(20))),
			delivered(true, timed(incomplete(head(5, sha("3"))), at(2), nil)),
		}},
		// The row is credited to the linking issue, as a stale attribution leaves it, so both of
		// that issue's arms read it.
		{"an answer whose attribution was incomplete over a row attributed to an issue", []foldStep{
			delivered(true, timed(credited(head(5, sha("1")), foldLinkingIssue), at(1), read(10))),
			learned(sha("2"), at(20), timed(incomplete(head(5, sha("2"))), at(2), nil)),
		}},
		// Both carry the same times, so the number orders them.
		{"a merged pull request learned and an open one stored after it", []foldStep{
			learned(sha("1"), at(20), timed(credited(merged(head(5, sha("1")), "release", sha("m")), foldIssue), at(1), read(20))),
			delivered(true, timed(credited(head(6, sha("6")), foldIssue), at(1), read(20))),
		}},
		// The second, of the pull request's next push, read no commit message: the issue stays
		// the first's, as the upsert keeps it when it stores both in turn.
		{"two answers learned for one pull request", []foldStep{
			learned(sha("1"), at(20), timed(credited(head(5, sha("1")), foldIssue), at(1), read(20))),
			learned(sha("2"), at(25), timed(incomplete(head(5, sha("2"))), at(2), nil)),
		}},
		// A row kept under the name the repository was renamed away from, and the answer of a read
		// asked under that name, keyed by the canonical one: the store drops the old row.
		{"an answer learned under a name its repository was renamed away from", []foldStep{
			delivered(true, timed(credited(foldHead(gadgetsOldRepo, 9, sha("7")), foldLinkingIssue), at(1), read(10))),
			learnedUnder(gadgetsOldRepo, sha("8"), at(20),
				timed(credited(foldHead(gadgetsRepo, 9, sha("8")), foldLinkingIssue), at(2), read(20))),
		}},
	} {
		t.Run(history.name, func(t *testing.T) {
			pool, ctx := deliveryTestPool(t)
			seedFoldIssues(t, ctx, pool)
			var answers CommitPullAnswers
			// standing holds, for each learned step, whether each of its heads still stands: no
			// writer's write after it changed its pull request's row.
			standing := make([][]bool, len(history.steps))
			for index, step := range history.steps {
				if step.writer == foldLearned {
					if err := foldLearn(ctx, pool, &answers, step); err != nil {
						t.Fatalf("step %d, %s: %v", index, step.writer, err)
					}
					standing[index] = make([]bool, len(step.pulls.Heads))
					for kept := range standing[index] {
						standing[index][kept] = true
					}
					continue
				}
				before := foldRowVersions(t, ctx, pool, step.pulls.Heads)
				foldWrite(t, ctx, pool, step)
				after := foldRowVersions(t, ctx, pool, step.pulls.Heads)
				for written, head := range step.pulls.Heads {
					if changed := before[written] != after[written]; changed != step.changes {
						t.Fatalf("step %d, %s of %s#%d: the row's xmin went from %q to %q, so its write changed it: %v, want %v",
							index, step.writer, head.Repo, head.Number, before[written], after[written], changed, step.changes)
					}
					if !step.changes {
						continue
					}
					for earlier := range index {
						for kept, keptHead := range history.steps[earlier].pulls.Heads {
							if standing[earlier] != nil && keptHead.Repo == head.Repo && keptHead.Number == head.Number {
								standing[earlier][kept] = false
							}
						}
					}
				}
			}
			questions := foldQuestions(t, history.steps)
			want := foldOracle(t, ctx, pool, history.steps, standing, questions)
			for _, question := range questions {
				got, err := foldReadOnly(ctx, pool, &answers, question)
				if err != nil {
					t.Errorf("%s, the read-only read: %v", question, err)
					continue
				}
				if got != want[question.String()] {
					t.Errorf("%s: the read-only read answered\n\t%s\nwant the writer's path's\n\t%s", question, got, want[question.String()])
				}
			}
		})
	}
}

// foldHead is pull request number of repo at commit, open, credited to nobody by a read of every
// attribution source and carrying no time.
func foldHead(repo string, number int, commit string) PullRequestHead {
	return PullRequestHead{
		Repo: repo, Number: number, HeadSHA: commit, HeadRef: "legion/work", BaseRef: "main", State: "open",
		AttributionSource: "none", AttributionComplete: true,
		Title: fmt.Sprintf("Pull request %d", number), URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
	}
}

// seedFoldIssues writes foldIssue, of deliveryRepo, and foldLinkingIssue, of gadgetsRepo, which
// gadgetsOldRepo was renamed to, and links foldLinkingIssue to deliveryRepo's pull request 5.
func seedFoldIssues(t *testing.T, ctx context.Context, pool *store.Pool) {
	t.Helper()
	if key := seedHeadIssue(t, ctx, pool, 701); key != foldIssue {
		t.Fatalf("seeded %s, want %s", key, foldIssue)
	}
	seedRenamedGadgets(t, ctx, pool)
	if key := seedGadgetsIssue(t, ctx, pool, 702); key != foldLinkingIssue {
		t.Fatalf("seeded %s, want %s", key, foldLinkingIssue)
	}
	link := "https://github.com/" + deliveryRepo + "/pull/5"
	if _, err := pool.Exec(ctx, `
		insert into issue_external_links (issue_key, url, kind) values ($1, $2, 'url')
	`, foldLinkingIssue, link); err != nil {
		t.Fatalf("link %s to %s: %v", foldLinkingIssue, link, err)
	}
	linked, err := LinkedPullRequests(ctx, pool, foldLinkingIssue)
	if err != nil || len(linked) != 1 || linked[0] != (LinkedPullRequest{Repo: deliveryRepo, Number: 5}) {
		t.Fatalf("the pull requests %s links = %v, %v; want %s#5 alone", foldLinkingIssue, linked, err, deliveryRepo)
	}
}

// foldWrite runs a writer's step through the writer's own statement.
func foldWrite(t *testing.T, ctx context.Context, pool *store.Pool, step foldStep) {
	t.Helper()
	switch step.writer {
	case foldDelivery:
		for _, head := range step.pulls.Heads {
			if err := UpsertPullRequestHead(ctx, pool, nil, head); err != nil {
				t.Fatalf("%s of %s#%d: %v", step.writer, head.Repo, head.Number, err)
			}
		}
	case foldReadThrough:
		if _, err := StoreCommitPulls(ctx, pool, nil, step.pulls); err != nil {
			t.Fatalf("%s: %v", step.writer, err)
		}
	default:
		t.Fatalf("%s writes nothing", step.writer)
	}
}

// foldRowVersions is the xmin of the row stored for the pull request of each of heads, under the
// name the head carries, and "" where no row is stored.
func foldRowVersions(t *testing.T, ctx context.Context, pool *store.Pool, heads []PullRequestHead) []string {
	t.Helper()
	versions := make([]string, len(heads))
	for index, head := range heads {
		err := pool.QueryRow(ctx, `
			select xmin::text from pull_request_heads where repo = $1 and number = $2
		`, head.Repo, head.Number).Scan(&versions[index])
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read the row of %s#%d: %v", head.Repo, head.Number, err)
		}
	}
	return versions
}

// foldQuestions is every question the fold test asks of each read of resultsHeadsReads in a
// history: the heads of each issue, whether each commit of each repository a step names heads a
// stored pull request, the merged branch of each issue and of foldLinkingIssue in deliveryRepo,
// and the newest head of each issue. A read it has no question for fails the test.
func foldQuestions(t *testing.T, steps []foldStep) []foldQuestion {
	t.Helper()
	var questions []foldQuestion
	for _, read := range resultsHeadsReads {
		switch read.read {
		case "headsAttributedQuery", "BaseBranchMergedFor", "NewestPullRequestHead":
			for _, issue := range []string{foldIssue, foldLinkingIssue} {
				questions = append(questions, foldQuestion{read: read.read, args: []string{issue}})
			}
		case "HeadStoredAt":
			asked := map[[2]string]bool{}
			for _, step := range steps {
				for _, head := range step.pulls.Heads {
					if commit := [2]string{head.Repo, head.HeadSHA}; !asked[commit] {
						asked[commit] = true
						questions = append(questions, foldQuestion{read: read.read, args: commit[:]})
					}
				}
			}
		case "baseBranchMergedInQuery":
			questions = append(questions, foldQuestion{read: read.read, args: []string{foldLinkingIssue, deliveryRepo}})
		default:
			t.Fatalf("the fold test asks nothing of the read %s", read.read)
		}
	}
	return questions
}

// foldOracle is the writer's path's answer to each question, by its String: D30's rule applied
// with the writer's own statements. Where no learned head stands (standing), the writer's read
// runs on the writers' rows. Otherwise, in one transaction rolled back once the reads have run,
// the stored rows are deleted and the whole history is run through upsertHeadRow, each learned
// head that stands among the writers' writes as a writer's read-through would have stored it then,
// and a read-through's or a learned answer's old-name row dropped as upsertHeadUnderCanonicalName
// drops it; then the writer's read runs in the transaction.
func foldOracle(t *testing.T, ctx context.Context, pool *store.Pool, steps []foldStep, standing [][]bool, questions []foldQuestion) map[string]string {
	t.Helper()
	answers := map[string]string{}
	stands := false
	for _, heads := range standing {
		for _, kept := range heads {
			stands = stands || kept
		}
	}
	var q refs.Queryer = pool
	if stands {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("open the oracle's transaction: %v", err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := tx.Exec(ctx, `delete from pull_request_heads`); err != nil {
			t.Fatalf("clear the oracle's rows: %v", err)
		}
		for index, step := range steps {
			for written, head := range step.pulls.Heads {
				if step.writer == foldLearned && !standing[index][written] {
					continue
				}
				if _, err := upsertHeadRow(ctx, tx, head); err != nil {
					t.Fatalf("the oracle's step %d, %s of %s#%d: %v", index, step.writer, head.Repo, head.Number, err)
				}
				if step.writer != foldDelivery && !strings.EqualFold(step.pulls.storedUnder, head.Repo) {
					if _, err := tx.Exec(ctx, `delete from pull_request_heads where repo = $1 and number = $2`,
						step.pulls.storedUnder, head.Number); err != nil {
						t.Fatalf("the oracle's step %d, drop %s#%d: %v", index, step.pulls.storedUnder, head.Number, err)
					}
				}
			}
		}
		q = tx
	}
	for _, question := range questions {
		answer, err := foldWriterRead(ctx, q, question)
		if err != nil {
			t.Fatalf("%s, the writer's read: %v", question, err)
		}
		answers[question.String()] = answer
	}
	return answers
}

// The fold test reaches the read-only reads, and the writer's reads its oracle runs, through the
// three functions below alone, so the histories can be run against another form of the reads by
// changing these and nothing else.

// foldLearn keeps step, an answer a read-only read learned, in answers as the production code
// keeps one (api's comparisonReader.learnCommitPulls): what is stored for its pull requests, read
// right after GitHub answered (ReadStoredRows), then Keep.
func foldLearn(ctx context.Context, pool *store.Pool, answers *CommitPullAnswers, step foldStep) error {
	pulls, err := ReadStoredRows(ctx, pool, step.pulls)
	if err != nil {
		return err
	}
	answers.Keep(step.pulls.storedUnder, step.sha, pulls, step.at)
	return nil
}

// foldReadOnly asks question of the read-only entry point of its read, over the heads answers
// keeps.
func foldReadOnly(ctx context.Context, pool *store.Pool, answers *CommitPullAnswers, question foldQuestion) (string, error) {
	kept, args := answers.Learned(), question.args
	switch question.read {
	case "headsAttributedQuery":
		heads, err := ReadOnlyHeadsAttributedTo(ctx, pool, args[0], kept)
		return describeAttributedHeads(heads), err
	case "HeadStoredAt":
		stored, err := ReadOnlyHeadStoredAt(ctx, pool, args[0], args[1], kept)
		return fmt.Sprint(stored), err
	case "BaseBranchMergedFor":
		branch, err := ReadOnlyBaseBranchMergedFor(ctx, pool, args[0], kept)
		return fmt.Sprintf("%q", branch), err
	case "baseBranchMergedInQuery":
		branch, err := ReadOnlyBaseBranchMergedIn(ctx, pool, args[0], args[1], kept)
		return fmt.Sprintf("%q", branch), err
	case "NewestPullRequestHead":
		head, found, err := ReadOnlyNewestPullRequestHead(ctx, pool, args[0], kept)
		return describeNewestHead(head, found), err
	}
	return "", fmt.Errorf("no read-only read answers %s", question.read)
}

// foldWriterRead asks question of the writer's form of its read, on q: the pool, or the oracle's
// transaction.
func foldWriterRead(ctx context.Context, q refs.Queryer, question foldQuestion) (string, error) {
	args := question.args
	switch question.read {
	case "headsAttributedQuery":
		heads, err := headsAttributedTo(ctx, q, headsAttributedRead.writer, args[0])
		return describeAttributedHeads(heads), err
	case "HeadStoredAt":
		stored, err := headStoredAt(ctx, q, headStoredAtRead.writer, args[0], args[1])
		return fmt.Sprint(stored), err
	case "BaseBranchMergedFor":
		branch, err := baseBranchMergedFor(ctx, q, baseBranchMergedForRead.writer, args[0])
		return fmt.Sprintf("%q", branch), err
	case "baseBranchMergedInQuery":
		branch, err := baseBranchMergedIn(ctx, q, baseBranchMergedInRead.writer, args[0], args[1])
		return fmt.Sprintf("%q", branch), err
	case "NewestPullRequestHead":
		head, found, err := newestPullRequestHead(ctx, q, newestPullRequestHeadRead.writer, args[0])
		return describeNewestHead(head, found), err
	}
	return "", fmt.Errorf("no writer's read answers %s", question.read)
}

// describeAttributedHeads is heads in short, in their order: each pull request's number, head,
// two times and linked repository.
func describeAttributedHeads(heads []AttributedHead) string {
	described := make([]string, 0, len(heads))
	for _, head := range heads {
		described = append(described, fmt.Sprintf("#%d at %.7s read %s updated %s linked %q",
			head.Number, head.HeadSHA, describeFoldTime(head.CheckedAt), describeFoldTime(head.EventAt), head.LinkedRepo))
	}
	return "[" + strings.Join(described, "; ") + "]"
}

// describeNewestHead is NewestPullRequestHead's answer in short.
func describeNewestHead(head NewestHead, found bool) string {
	if !found {
		return "no head"
	}
	return fmt.Sprintf("%s#%d at %.7s", head.Repo, head.Number, head.HeadSHA)
}

// describeFoldTime is a stored time in UTC, and "-" for none.
func describeFoldTime(moment *time.Time) string {
	if moment == nil {
		return "-"
	}
	return moment.UTC().Format(time.TimeOnly)
}
