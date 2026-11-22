package specgraph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// The kinds of state that the sweeps judge, by the form in which the reader writes the place of
// each (sweepreader's sweepStates): "subject <n> check <id>", "subject <n> statement <id>",
// "subject <n> own state of <id>" and "subject <n> wait of <unit> on <id>". A statement and its own
// state are two places.
const (
	stateCheck     = "a check"
	stateStatement = "a statement"
	stateOwn       = "a statement's own state"
	stateWait      = "a wait"
)

// judgedKind is one kind of state that the judgment judges (judgedKinds): its name, the words of
// its place after the subject, its states, and top, the one of them that a move toward true reads.
type judgedKind struct {
	name, words string
	states      []string
	top         string
}

// judgedKinds is each kind of state that the reader writes, with its states as the reader's code
// declares them, which TestTheSweepsJudgmentAgreesWithItsDefinitionOverItsWholeDomain reads: a check
// true, false or unknown (checkstate.go); a statement and its own state not_built, met, not_met or
// unknown (statementstate.go); and a wait ends or holds (sweepreader's main.go).
func judgedKinds() []judgedKind {
	statement := []string{statementNotBuilt, statementMet, statementNotMet, statementUnknown}
	return []judgedKind{
		{name: stateCheck, words: "check ", states: []string{checkTrue, checkFalse, checkUnknown}, top: checkTrue},
		{name: stateStatement, words: "statement ", states: statement, top: statementMet},
		{name: stateOwn, words: "own state of ", states: slices.Clone(statement), top: statementMet},
		{name: stateWait, words: "wait of ", states: []string{"ends", "holds"}, top: "ends"},
	}
}

// placeKindOf is the kind, of kinds, of the state at the place at, by the form of the place, and
// whether the place is of one.
func placeKindOf(kinds []judgedKind, at string) (judgedKind, bool) {
	words := strings.SplitN(at, " ", 3)
	if len(words) != 3 || words[0] != "subject" {
		return judgedKind{}, false
	}
	if _, err := strconv.Atoi(words[1]); err != nil {
		return judgedKind{}, false
	}
	for _, k := range kinds {
		if strings.HasPrefix(words[2], k.words) {
			return k, true
		}
	}
	return judgedKind{}, false
}

// The forms of an input's right at one place (rightAt): no right; a right that names no place,
// which covers each place, so that its input is judged by its right alone (pathOfOneSpace,
// parentLeftOut); and of a right that names its places, a place that it leaves out, and a place that
// it names, with the one state that it allows there.
const (
	rightNone    = "no right"
	rightEach    = "a right that names no place"
	rightLeftOut = "a right that leaves the place out"
	rightNamed   = "a right that names the place"
)

// rightRules is the judgment's rule for each form of an input's right at one place: whether the right
// allows the state that the input reads there, input. No right and a place that a right leaves out
// allow no state; a right that names no place allows each; a right that names the place allows the
// one state that it names there. It is the one list of the forms: movedBeyond judges a form by its
// rule here and refuses a form that has none, and rightForms, which the domain test generates the
// rights from, is its forms. So a form that the judgment reads is a form that the domain test
// generates, and the definition's silence on a new one fails that test by name.
func rightRules() map[string]func(right placeRight, input string) bool {
	return map[string]func(placeRight, string) bool{
		rightNone:    func(placeRight, string) bool { return false },
		rightEach:    func(placeRight, string) bool { return true },
		rightLeftOut: func(placeRight, string) bool { return false },
		rightNamed:   func(right placeRight, input string) bool { return right.state == input },
	}
}

// rightForms is each form of an input's right at one place: the forms that the judgment has a rule
// for (rightRules), in the order of their words.
func rightForms() []string { return slices.Sorted(maps.Keys(rightRules())) }

// placeRight is what an input's right holds at one place: its form (rightForms), and for a right
// that names the place, the state that it allows there.
type placeRight struct{ form, state string }

// rightAt is the right at the place at of an input with a right where hasRight holds, and with none
// where it does not, whose right allows at each place that it names the state that allowed holds
// there, allowed nil for a right that names no place.
func rightAt(hasRight bool, allowed map[string]string, at string) placeRight {
	switch state, named := allowed[at]; {
	case !hasRight:
		return placeRight{form: rightNone}
	case allowed == nil:
		return placeRight{form: rightEach}
	case !named:
		return placeRight{form: rightLeftOut}
	default:
		return placeRight{form: rightNamed, state: state}
	}
}

// movedBeyond is the judgment of the sweeps at one place: whether the input's state there, input,
// moved toward true beyond the input's right there, right, from base, the state that the base reads
// there ("" where it reads none), for a state of the kind kind. A move toward true reads the kind's
// top where the base reads another state or none, and it is beyond the right unless the right's rule
// (rightRules) allows the input's state there. It reads its arguments alone: no base, no words of an
// input or reason of its right, and of the place only its kind. A form of right that has no rule, and
// a state that is no state of the kind, at the input, the base or the right, is an error, so a sweep
// fails by name where the reader writes a state of no kind or a right reads a form that the judgment
// has no rule for. Its callers are movesBeyond and
// TestTheSweepsJudgmentAgreesWithItsDefinitionOverItsWholeDomain, which holds it to definedBeyond at
// each kind, each state that the base reads and none, each state that the input reads, and each form
// of right with each state of the kind.
func movedBeyond(kind judgedKind, base, input string, right placeRight) (bool, error) {
	allows, ruled := rightRules()[right.form]
	switch {
	case !ruled:
		return false, fmt.Errorf("the right is of the form %q, which the judgment has no rule for (rightRules)", right.form)
	case !slices.Contains(kind.states, input):
		return false, fmt.Errorf("the input reads %q, which is no state of %s (judgedKinds)", input, kind.name)
	case base != "" && !slices.Contains(kind.states, base):
		return false, fmt.Errorf("the base reads %q, which is no state of %s (judgedKinds)", base, kind.name)
	case right.form == rightNamed && !slices.Contains(kind.states, right.state):
		return false, fmt.Errorf("the right allows %q, which is no state of %s (judgedKinds)", right.state, kind.name)
	}
	toward := input == kind.top && base != input
	return toward && !allows(right, input), nil
}

// movesBeyond is the judgment of one answer of the reader, got, against the states of the base,
// whole: the line of each place of got where movedBeyond reports a move toward true beyond the
// input's right (hasRight and allowed, as rightAt reads them), "<place>: <got's state>, and <whole's
// state> in the whole scenario", in the order of the places, so with no right each place that moved
// toward true; or an error that names the place of no kind, or the state of no kind, that it met. It
// reads the two answers and the right's places alone: no base, no words of an input and no reason
// of its right. Its one caller is judgeAnswer.
func movesBeyond(whole, got map[string]string, hasRight bool, allowed map[string]string) ([]string, error) {
	kinds := judgedKinds()
	var moved []string
	for _, at := range slices.Sorted(maps.Keys(got)) {
		kind, of := placeKindOf(kinds, at)
		if !of {
			return nil, fmt.Errorf("the place %q is of no kind of state that the judgment knows (judgedKinds)", at)
		}
		beyond, err := movedBeyond(kind, whole[at], got[at], rightAt(hasRight, allowed, at))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		if beyond {
			moved = append(moved, fmt.Sprintf("%s: %q, and %q in the whole scenario", at, got[at], whole[at]))
		}
	}
	return moved, nil
}

// judgeAnswer is the judgment of the reader's answer to one input, as sweepAgainst hands it over,
// unchanged, for each input that reads another state than its base: whole, the states of the base;
// got, the input's; and the input's right, right ("" for none), with allowed, each place that it
// names with the one state that it allows there (nil for a right that names no place). It gives
// toward, the line of each place that moved toward true (movesBeyond with no right), and beyond, the
// line of each that moved toward true beyond the input's right (movesBeyond with that right, and
// toward where the input has none), or the error of either. Its callers are sweepAgainst and
// TestTheSweepsJudgmentAgreesWithItsDefinitionOverItsWholeDomain, which hands it whole answers of
// each base (eachAnswer) and holds each to the definition applied place by place (definedAnswer).
func judgeAnswer(whole, got map[string]string, right string, allowed map[string]string) (toward, beyond []string, err error) {
	toward, err = movesBeyond(whole, got, false, nil)
	beyond = toward
	if err == nil && right != "" {
		beyond, err = movesBeyond(whole, got, true, allowed)
	}
	return toward, beyond, err
}

// definedBeyond is the definition that the domain test holds the judgment (movedBeyond) to, written
// from the approved words apart from the judgment, with lists of its own: whether, at one place of a
// state of the kind named kind, the input that reads input where the base reads base ("" for none)
// moved toward true beyond the right right; and covered, whether its words name that case at all, so
// that a kind, a state or a form of right that they do not name fails the domain test by name.
// TestTheDefinitionOfAMoveBeyondARightReadsAsTheSpec holds it to rows that the approved words decide.
func definedBeyond(kind, base, input string, right placeRight) (beyond, covered bool) {
	// The states of each kind, its true state first. R7: a check's state is "true, false, or unknown
	// with a reason". R10: "Each statement shows one of four states, for one subject at a time: not
	// built, met, not met, or unknown"; "Met: its own checks read true, and statements under it or
	// that support it read met". R29: a wait on a statement "ends when main holds the statement's
	// approved text and its own checks read true there. The statements under it do not count for that
	// wait": the statement's own state, met where its own checks read true, and the wait, which holds
	// until it ends.
	states, known := map[string][]string{
		stateCheck:     {"true", "false", "unknown"},
		stateStatement: {"met", "not_built", "not_met", "unknown"},
		stateOwn:       {"met", "not_built", "not_met", "unknown"},
		stateWait:      {"ends", "holds"},
	}[kind]
	if !known || !slices.Contains(states, input) || base != "" && !slices.Contains(states, base) {
		return false, false
	}
	isTrue := func(state string) bool { return state == states[0] }
	// A move toward true: the input reads the true state of its kind, and the base reads another, or
	// none at that place. R9: "Six cases read as unknown, and never as true"; R7: "A step that left no
	// result reads as unknown: silence is not a pass".
	toward := isTrue(input) && !isTrue(base)
	switch right.form {
	case rightNone:
		// No right, so nothing allows the move.
		return toward, true
	case rightEach:
		// A right that names no place covers each place, as a path of a tree written " " ends each
		// carry across it (R8: "A result at one commit also counts at another commit when no code that
		// the check runs changed between the two").
		return false, true
	case rightLeftOut:
		// A right that names its places allows nothing at a place that it leaves out.
		return toward, true
	case rightNamed:
		if !slices.Contains(states, right.state) {
			return false, false
		}
		// A right allows at a place that it names the one state that it names there, as a refusal of
		// CI names the states with its commit's results taken out (R48: "At that commit, such a check
		// reads unknown, and no earlier result of it counts"): a move to true is within it only where
		// that state is true.
		return toward && !isTrue(right.state), true
	}
	return false, false
}

// TestTheDefinitionOfAMoveBeyondARightReadsAsTheSpec holds the definition that the domain test holds
// the judgment to (definedBeyond) to rows that the approved words decide, each with its words.
func TestTheDefinitionOfAMoveBeyondARightReadsAsTheSpec(t *testing.T) {
	t.Parallel()
	named := func(state string) placeRight { return placeRight{form: rightNamed, state: state} }
	rows := []struct {
		words             string
		kind, base, input string
		right             placeRight
		beyond            bool
	}{
		{`R9: "Six cases read as unknown, and never as true"`, stateCheck, "unknown", "true", placeRight{form: rightNone}, true},
		{`R7: "a later failure cancels an earlier pass"; taking the failure out may read true where a right names true`, stateCheck, "false", "true", named("true"), false},
		{`R48: "At that commit, such a check reads unknown, and no earlier result of it counts": a right that names the check unknown allows no true`, stateCheck, "false", "true", named("unknown"), true},
		{`R7: "A step that left no result reads as unknown: silence is not a pass"; a place that the base does not hold`, stateCheck, "", "true", placeRight{form: rightNone}, true},
		{`R8: "A result at one commit also counts at another commit when no code that the check runs changed between the two"; a right that names no place`, stateCheck, "false", "true", placeRight{form: rightEach}, false},
		{`R10: "Not met: an own check reads false"; a place that the right leaves out`, stateStatement, "not_met", "met", placeRight{form: rightLeftOut}, true},
		{`R10: "Not built: the files at that commit do not hold the approved text of the statement"`, stateStatement, "not_built", "met", placeRight{form: rightNone}, true},
		{`R10: "Unknown: every other case"; a move from met is no move toward true`, stateStatement, "met", "unknown", placeRight{form: rightNone}, false},
		{`R29: "its own checks read true there. The statements under it do not count for that wait"`, stateOwn, "unknown", "met", named("met"), false},
		{`R29: "That wait ends when main holds the statement's approved text and its own checks read true there"`, stateWait, "holds", "ends", named("holds"), true},
		{`R29: a wait that ends at the base moves nowhere`, stateWait, "ends", "ends", placeRight{form: rightNone}, false},
	}
	for _, r := range rows {
		if got, covered := definedBeyond(r.kind, r.base, r.input, r.right); !covered || got != r.beyond {
			t.Errorf("%s: %s at %q, %q at the input, %s %q: the definition reads beyond %v (covered %v), want %v", r.words, r.kind, r.base, r.input, r.right.form, r.right.state, got, covered, r.beyond)
		}
	}
}

// rightsOf is each right at one place of a state of the kind k: each form that the judgment has a
// rule for (rightForms), and for a right that names the place, one for each state of k.
func rightsOf(k judgedKind) []placeRight {
	var rights []placeRight
	for _, form := range rightForms() {
		if form != rightNamed {
			rights = append(rights, placeRight{form: form})
			continue
		}
		for _, state := range k.states {
			rights = append(rights, placeRight{form: form, state: state})
		}
	}
	return rights
}

// answerRight is the words of the right of each input that the domain test writes with a right: the
// judgment reads a right's words for whether there is one and nothing else (judgeAnswer).
const answerRight = "the right of an answer that the domain test writes"

// answerRights is each right of an input as a whole, by the forms that it gives its places (rightAt):
// no right, a right that names no place, and a right that names its places, which leaves each other
// place out. The domain test fails by name a form of the judgment's rules (rightForms) that none of
// them holds, so each form that the judgment reads is a form that the test writes in whole answers.
func answerRights() [][]string {
	return [][]string{{rightNone}, {rightEach}, {rightLeftOut, rightNamed}}
}

// answer is one answer that the domain test hands the judgment of an answer (judgeAnswer), as
// sweepAgainst hands one over: the states of the base, whole; the input's, got; and the input's right,
// right ("" for none), with allowed (nil for a right that names no place). rights is the right that
// the input has at each place of got, which the definition reads place by place (definedAnswer).
type answer struct {
	whole, got, allowed map[string]string
	right               string
	rights              map[string]placeRight
}

// newAnswer is the answer whose base reads whole and whose input reads got, with the right that
// rights gives at each place written as one right of the input: no right where each place has none, a
// right that names no place where each has that, and else a right that names each place that rights
// names, with its state, and leaves out each other. It is false where rights gives a form that no
// right of an answer holds (answerRights) or the forms of two of them.
func newAnswer(whole, got map[string]string, rights map[string]placeRight) (answer, bool) {
	forms := map[string]bool{}
	for _, r := range rights {
		forms[r.form] = true
	}
	a := answer{whole: whole, got: got, rights: rights}
	switch {
	case len(forms) == 1 && forms[rightNone]:
		return a, true
	case len(forms) == 1 && forms[rightEach]:
		a.right = answerRight
		return a, true
	}
	a.right, a.allowed = answerRight, map[string]string{}
	for at, r := range rights {
		switch r.form {
		case rightLeftOut:
		case rightNamed:
			a.allowed[at] = r.state
		default:
			return answer{}, false
		}
	}
	return a, true
}

// definedAnswer is the definition (definedBeyond) applied to the answer a place by place: the line of
// each place of a.got that moved toward true with no right, and of each that moved toward true beyond
// a's right there, as judgeAnswer writes them, in the order of the places; kinds is the kind of each
// place.
func definedAnswer(a answer, kinds map[string]judgedKind) (toward, beyond []string) {
	for _, at := range slices.Sorted(maps.Keys(a.got)) {
		kind, right := kinds[at].name, a.rights[at]
		towardHere, _ := definedBeyond(kind, a.whole[at], a.got[at], placeRight{form: rightNone})
		beyondHere, _ := definedBeyond(kind, a.whole[at], a.got[at], right)
		if !towardHere && !beyondHere {
			continue
		}
		line := fmt.Sprintf("%s: %q, and %q in the whole scenario", at, a.got[at], a.whole[at])
		if towardHere {
			toward = append(toward, line)
		}
		if beyondHere {
			beyond = append(beyond, line)
		}
	}
	return toward, beyond
}

// eachAnswer hands judge each answer that the domain test judges of one base, whose reader's answer
// is whole, each place of it of the kind that kinds gives, with the words of its case and where in the
// base it is. For each place, each case of its kind there (each state that the base reads and none,
// each state that the input reads, and each right at the place, rightsOf), with each other place as the
// base reads it, under the same right of the answer (answerRights), which names it with the base's
// state where that right names places. And for each right of an answer, answers that move each place
// at once: in the answer n, the place i takes the case n+i of its kind under that right, so that each
// place takes each case of its kind, beside other places' cases. A form of right that no right of an
// answer holds is in no answer, and the domain test fails it by name.
func eachAnswer(t *testing.T, whole map[string]string, kinds map[string]judgedKind, judge func(c, where string, a answer)) {
	t.Helper()
	places := slices.Sorted(maps.Keys(whole))
	type place struct {
		base, input string
		right       placeRight
	}
	// casesOf is each case of the kind k under the right of an answer that holds forms.
	casesOf := func(k judgedKind, forms []string) []place {
		var cases []place
		for _, base := range append([]string{""}, k.states...) {
			for _, input := range k.states {
				for _, right := range rightsOf(k) {
					if slices.Contains(forms, right.form) {
						cases = append(cases, place{base, input, right})
					}
				}
			}
		}
		return cases
	}
	// write hands judge the answer of the base with each place set to its case of cases.
	write := func(c, where string, cases map[string]place) {
		before, after, rights := maps.Clone(whole), maps.Clone(whole), map[string]placeRight{}
		for _, at := range places {
			p := cases[at]
			if p.base == "" {
				delete(before, at)
			} else {
				before[at] = p.base
			}
			after[at], rights[at] = p.input, p.right
		}
		a, ok := newAnswer(before, after, rights)
		if !ok {
			t.Errorf("%s: the domain test writes no input that holds the rights %v (newAnswer)", c, rights)
			return
		}
		judge(c, where, a)
	}
	for _, forms := range answerRights() {
		for _, at := range places {
			for _, focus := range casesOf(kinds[at], forms) {
				cases := map[string]place{}
				for _, other := range places {
					steady := placeRight{form: forms[0]}
					if slices.Contains(forms, rightNamed) {
						steady = placeRight{form: rightNamed, state: whole[other]}
					}
					cases[other] = place{whole[other], whole[other], steady}
				}
				cases[at] = focus
				write(cell(kinds[at].name, focus.base, focus.input, focus.right), "at "+at, cases)
			}
		}
	}
	for _, forms := range answerRights() {
		of, rounds := map[string][]place{}, 0
		for _, at := range places {
			of[at] = casesOf(kinds[at], forms)
			rounds = max(rounds, len(of[at]))
		}
		for n := range rounds {
			cases := map[string]place{}
			for i, at := range places {
				if len(of[at]) == 0 {
					continue
				}
				cases[at] = of[at][(n+i)%len(of[at])]
			}
			write(fmt.Sprintf("answers that move each place at once, with %s", strings.Join(forms, " or ")), fmt.Sprintf("answer %d", n), cases)
		}
	}
}

// declaredStates is the value of each constant of the block of the Go file file, a path under this
// package, whose doc comment is doc: the states of a kind as the reader's code declares them, read
// from the file that the build of the reader's binary reads (sweepReaderCommand, Source), the overlay
// of ENVOY_TEST_GO_OVERLAY included where it is set.
func declaredStates(t *testing.T, file, doc string) []string {
	t.Helper()
	src, err := sweepReaderCommand().Source(file)
	if err != nil {
		t.Fatalf("the reader's code: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("the reader's code: %v", err)
	}
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST || g.Doc == nil || strings.TrimSpace(g.Doc.Text()) != doc {
			continue
		}
		var states []string
		for _, spec := range g.Specs {
			for _, value := range spec.(*ast.ValueSpec).Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: the block %q holds a constant that is no string", file, doc)
				}
				state, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: the block %q: %v", file, doc, err)
				}
				states = append(states, state)
			}
		}
		return states
	}
	t.Fatalf("%s holds no block of constants whose doc comment is %q", file, doc)
	return nil
}

// cell is the words of one case of the judgment's domain, for a line of a failure.
func cell(kind, base, input string, right placeRight) string {
	if right.form == rightNamed {
		return fmt.Sprintf("%s at %q in the base and %q at the input, %s with %q", kind, base, input, right.form, right.state)
	}
	return fmt.Sprintf("%s at %q in the base and %q at the input, %s", kind, base, input, right.form)
}

// The judgment of the sweeps (movedBeyond at one place, judgeAnswer for the reader's answer to an
// input) is tested over its whole domain, against a definition written apart from it from the
// approved words (definedBeyond). The kinds of state and their states are the judgment's lists
// (judgedKinds), each held to the states that the reader's code declares: the constants of "The
// states of a check (R7)." in checkstate.go, of "The states of a statement (R10)." in
// statementstate.go, a statement's own state's too, and of "The states of a wait (R29)." in
// sweepreader's main.go, each read from the file that the build of the reader's binary reads, its
// overlay included (declaredStates). The test generates each case: each kind, each state that the
// base reads and none, each state that the input reads, and each form of right that the judgment has
// a rule for (rightForms, rightRules) with each state of the kind for a right that names the place;
// it fails where the definition does not cover a case, where the judgment refuses one, and where the
// two disagree, by the case. So a state, a kind or a form of right that the code gains with no word of
// the definition for it fails by name. Outside the domain, the judgment refuses a state of no kind, a
// place of no kind and a form of right that it has no rule for. And it reads each base of the sweeps
// (sweepBases) in the reader's process: each place of each base is of a kind and each state that it
// reads is a state of that kind; and it hands judgeAnswer whole answers of each base, as sweepAgainst
// hands it the reader's answer and the input's right (eachAnswer): each case of its kind at each
// place, with each other place as the base reads it, and answers that move each place at once, each
// under each right of an answer; and it holds what judgeAnswer reports of each to the definition
// applied place by place (definedAnswer). So a judgment of an answer that drops or adds a place, or
// reads another right at a place than the input's, in those answers, fails by the case. It does not
// show that sweepAgainst hands judgeAnswer the reader's answer and the input's right; the known
// positives (knownPositives) show that at their bases.
func TestTheSweepsJudgmentAgreesWithItsDefinitionOverItsWholeDomain(t *testing.T) {
	t.Parallel()
	kinds := judgedKinds()
	statement := declaredStates(t, "statementstate.go", "The states of a statement (R10).")
	declared := map[string][]string{
		stateCheck:     declaredStates(t, "checkstate.go", "The states of a check (R7)."),
		stateStatement: statement,
		stateOwn:       statement,
		stateWait:      declaredStates(t, filepath.Join("internal", "sweepreader", "main.go"), "The states of a wait (R29)."),
	}
	for _, k := range kinds {
		switch want, held := declared[k.name]; {
		case !held:
			t.Errorf("the judgment knows %s, a kind whose states the reader's code does not declare", k.name)
		case !slices.Equal(slices.Sorted(slices.Values(k.states)), slices.Sorted(slices.Values(want))):
			t.Errorf("the judgment reads %s's states as %q, and the reader's code declares %q", k.name, k.states, want)
		case !slices.Contains(k.states, k.top):
			t.Errorf("the judgment reads %s's move toward true as %q, which is none of its states %q", k.name, k.top, k.states)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		if !slices.ContainsFunc(kinds, func(k judgedKind) bool { return k.name == name }) {
			t.Errorf("the reader's code declares the states of %s, which the judgment does not know (judgedKinds)", name)
		}
	}
	cases := 0
	for _, k := range kinds {
		for _, base := range append([]string{""}, k.states...) {
			for _, input := range k.states {
				for _, right := range rightsOf(k) {
					cases++
					got, err := movedBeyond(k, base, input, right)
					want, covered := definedBeyond(k.name, base, input, right)
					switch {
					case !covered:
						t.Errorf("%s: the definition (definedBeyond) does not cover it", cell(k.name, base, input, right))
					case err != nil:
						t.Errorf("%s: the judgment (movedBeyond) refuses it: %v", cell(k.name, base, input, right), err)
					case got != want:
						t.Errorf("%s: the judgment (movedBeyond) reads beyond the right %v, and the definition (definedBeyond) %v", cell(k.name, base, input, right), got, want)
					}
				}
			}
		}
	}
	// Outside the domain: the judgment refuses each case, and the definition covers none.
	check := judgedKind{}
	for _, k := range kinds {
		if k.name == stateCheck {
			check = k
		}
	}
	for _, out := range []struct {
		what              string
		kind, base, input string
		right             placeRight
	}{
		{"an input that reads a state of no kind", stateCheck, "", statementMet, placeRight{form: rightNone}},
		{"a base that reads a state of no kind", stateCheck, statementMet, checkTrue, placeRight{form: rightNone}},
		{"a right that allows a state of no kind", stateCheck, "", checkTrue, placeRight{form: rightNamed, state: statementMet}},
		{"a form of right that the judgment has no rule for", stateCheck, "", checkTrue, placeRight{form: "a right that names the place twice"}},
		{"a kind that no list holds", "a deploy", "", checkTrue, placeRight{form: rightNone}},
	} {
		if _, err := movedBeyond(check, out.base, out.input, out.right); err == nil && out.kind == stateCheck {
			t.Errorf("%s: the judgment (movedBeyond) reads it", out.what)
		}
		if _, covered := definedBeyond(out.kind, out.base, out.input, out.right); covered {
			t.Errorf("%s: the definition (definedBeyond) covers it", out.what)
		}
	}
	if _, _, err := judgeAnswer(map[string]string{}, map[string]string{"subject 1 note of c": checkTrue}, "", nil); err == nil {
		t.Errorf("a place of no kind: the judgment of an answer (judgeAnswer) reads it")
	}
	// Each form of the judgment's rules is in one right of an answer that the test writes, and each
	// form that the test writes in answers is one that the judgment has a rule for.
	written := slices.Concat(answerRights()...)
	for _, form := range rightForms() {
		if n := len(slices.DeleteFunc(slices.Clone(written), func(f string) bool { return f != form })); n != 1 {
			t.Errorf("the judgment has a rule for the form %q, which %d rights of an answer that the domain test writes hold, not one (answerRights)", form, n)
		}
	}
	for _, form := range written {
		if !slices.Contains(rightForms(), form) {
			t.Errorf("the domain test writes answers with the form %q, which the judgment has no rule for (rightRules)", form)
		}
	}
	bases := sweepBases(t)
	p := newSweepReader(t).start(t)
	places, answers := 0, 0
	disagree := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		whole := baseStates(t, p, bases[name])
		kindOf := map[string]judgedKind{}
		for _, at := range slices.Sorted(maps.Keys(whole)) {
			switch k, of := placeKindOf(kinds, at); {
			case !of:
				t.Errorf("%s: the reader writes the place %q, which is of no kind of state that the judgment knows (judgedKinds)", name, at)
			case !slices.Contains(k.states, whole[at]):
				t.Errorf("%s: the reader reads %q at %s, which is no state of %s (judgedKinds)", name, whole[at], at, k.name)
			default:
				kindOf[at] = k
			}
		}
		if len(kindOf) < len(whole) {
			continue
		}
		places += len(whole)
		eachAnswer(t, whole, kindOf, func(c, where string, a answer) {
			answers++
			wantToward, wantBeyond := definedAnswer(a, kindOf)
			toward, beyond, err := judgeAnswer(a.whole, a.got, a.right, a.allowed)
			if err != nil || !slices.Equal(toward, wantToward) || !slices.Equal(beyond, wantBeyond) {
				disagree[c] = append(disagree[c], fmt.Sprintf("%s, %s: the judgment gives %q toward true and %q beyond the right (%v), and the definition %q and %q", name, where, toward, beyond, err, wantToward, wantBeyond))
			}
		})
	}
	for _, c := range slices.Sorted(maps.Keys(disagree)) {
		t.Errorf("%s: the judgment of an answer (judgeAnswer) disagrees with the definition applied place by place (definedAnswer) in %d answers, the first %s", c, len(disagree[c]), disagree[c][0])
	}
	t.Logf("%d cases of %d kinds of state; %d places of %d bases, %d whole answers judged", cases, len(kinds), places, len(bases), answers)
}

// knownRight is the right of the known positive beyond its right (knownPositives): it holds at each
// place the state that the base reads there, but at places that the input moves, which it leaves out
// or names with a state that is neither the base's nor the input's, so that each move of the input is
// beyond it.
const knownRight = "a right that holds at each place the state that the base reads there, but at places that the input moves, which it leaves out or names with a state that is neither the base's nor the input's, so that each move is beyond it"

// The words of the known positives (knownPositives).
const (
	knownWait       = "the known positive of a wait, results.json /4 (the failing run of a-restart-keeps-the-message) with the outcome pass"
	knownOutOrOther = "the known positive beyond its right at places that it leaves out or names with another state, jobs.json /1 (the leg arm of go-checks at m2) taken out"
)

// knownPositives is each input that a sweep must report at the base name, with moves, the lines of
// the states that it is built to move toward true, as judgeAnswer writes them, in their order. The
// domain test holds the judgment of an answer (judgeAnswer) to its definition over whole answers of
// each base; what the known positives show, and it does not, is that each sweep calls judgeAnswer at
// their bases with the reader's answer and a right no wider than the input's at the places that they
// move, and fails by their lines (sweepAgainst): a sweep that skips the judgment for them or counts
// one as read, or hands it the base's states for the reader's answer, fails by both names, and one
// that hands it a right wider than the input's at those places fails by the second. A sweep that
// hands it no right, or a right narrower than the input's, fails by the inputs that move a state
// toward true by their right, not by these. One for each kind of input that the judgment can report:
// with no right, and with a right that names its places; each moves each kind of state. At
// wait-cycle, the failing run of a-restart-keeps-the-message (results.json /4) with the outcome
// pass, with no right: that check reads true, a-message-survives-a-restart, its own state and
// envoy-delivers-messages read met, and the wait of u-b on a-message-survives-a-restart ends. It
// shows that each sweep hands the judgment the reader's answer. And at silent-leg, the leg arm of
// go-checks at m2 (jobs.json /1), which ended with no result of the check, taken out, with the right
// knownRight, which leaves out each place that the input moves at the first subject and names each
// that it moves at the third with a state that is neither the base's nor the input's (false for the
// check, not_met for the statements and the own state): the check a-restart-loses-no-message, which
// silent-leg reads unknown at the first and third subjects, reads true there,
// a-stored-message-survives-a-restart, its own state and messages-reach-sessions read met there, and
// the wait of u-a on a-stored-message-survives-a-restart ends at the first subject. It shows that
// each sweep hands the judgment a right no wider than the input's at the places that the input
// moves: a right that names no place, or that allows the input's state at one of them, would not
// report it. whole is the states of the base.
func knownPositives(t *testing.T, name string, base fstest.MapFS, whole map[string]string) []sweepInput {
	t.Helper()
	switch name {
	case "silent-leg":
		outOrOther := maps.Clone(whole)
		for _, at := range []string{
			"subject 1 check a-restart-loses-no-message",
			"subject 1 own state of a-stored-message-survives-a-restart",
			"subject 1 statement a-stored-message-survives-a-restart",
			"subject 1 statement messages-reach-sessions",
			"subject 1 wait of u-a on a-stored-message-survives-a-restart",
		} {
			delete(outOrOther, at)
		}
		outOrOther["subject 3 check a-restart-loses-no-message"] = checkFalse
		for _, at := range []string{
			"subject 3 own state of a-stored-message-survives-a-restart",
			"subject 3 statement a-stored-message-survives-a-restart",
			"subject 3 statement messages-reach-sessions",
		} {
			outOrOther[at] = statementNotMet
		}
		return []sweepInput{{
			what:  knownOutOrOther,
			fsys:  changedFile(t, base, "jobs.json", treeChange{place: []string{"1"}, out: true}, leftOutAt),
			right: knownRight, allowed: outOrOther, known: true, moves: []string{
				`subject 1 check a-restart-loses-no-message: "true", and "unknown" in the whole scenario`,
				`subject 1 own state of a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
				`subject 1 statement a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
				`subject 1 statement messages-reach-sessions: "met", and "unknown" in the whole scenario`,
				`subject 1 wait of u-a on a-stored-message-survives-a-restart: "ends", and "holds" in the whole scenario`,
				`subject 3 check a-restart-loses-no-message: "true", and "unknown" in the whole scenario`,
				`subject 3 own state of a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
				`subject 3 statement a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
				`subject 3 statement messages-reach-sessions: "met", and "unknown" in the whole scenario`,
			},
		}}
	case "wait-cycle":
		return []sweepInput{{
			what:  knownWait,
			fsys:  changedFile(t, base, "results.json", treeChange{place: []string{"4", "outcome"}, value: "pass"}, valueAt),
			known: true, moves: []string{
				`subject 1 check a-restart-keeps-the-message: "true", and "false" in the whole scenario`,
				`subject 1 own state of a-message-survives-a-restart: "met", and "not_met" in the whole scenario`,
				`subject 1 statement a-message-survives-a-restart: "met", and "not_met" in the whole scenario`,
				`subject 1 statement envoy-delivers-messages: "met", and "not_met" in the whole scenario`,
				`subject 1 wait of u-b on a-message-survives-a-restart: "ends", and "holds" in the whole scenario`,
			},
		}}
	}
	return nil
}
