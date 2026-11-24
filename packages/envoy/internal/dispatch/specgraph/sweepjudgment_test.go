package specgraph

import (
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"reflect"
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
// each kind, each state that the base reads and none, each value that the input reads (judgedValues),
// and each form of right with each value that it names a place with.
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

// judgeAnswer is the judgment of the reader's answer to one input, as sweepAgainst hands it over for
// each input that the reader reads: whole, the reader's reply for the base, and got, its reply for the
// input, each as the reader's process wrote it, with the key of the line that it answers (readerReply,
// whose states judgeAnswer reads itself); hasRight, whether the input has a right; and allowed, each
// place that its right names with the one state that it allows there (nil for a right that names no
// place). Of a right it reads whether there is one and its places, and no words. It gives toward, the
// line of each place that moved toward true (movesBeyond with no right), and beyond, the line of each
// that moved toward true beyond the input's right (movesBeyond with that right, which is toward where
// the input has none), or the error of either, or of a reply that gives no states; and answered, the keys
// of the lines that whole and got answer, in that order, by which sweepAgainst counts the input as read
// (counted), so the count holds both replies that the judgment read for the input, the base's and the
// input's, and no other. Its callers are sweepAgainst and
// TestTheSweepsJudgmentAgreesWithItsDefinitionOverItsWholeDomain, which hands it answers of each base
// whose arguments it ranges over by their types (eachAnswer, replyOf) and holds each to the definition
// applied place by place (definedAnswer).
func judgeAnswer(whole, got readerReply, hasRight bool, allowed map[string]string) (toward, beyond []string, answered [2][sha256.Size]byte, err error) {
	answered = [2][sha256.Size]byte{whole.line, got.line}
	base, err := whole.states()
	if err != nil {
		return nil, nil, answered, fmt.Errorf("the base: %w", err)
	}
	input, err := got.states()
	if err != nil {
		return nil, nil, answered, fmt.Errorf("the input: %w", err)
	}
	toward, err = movesBeyond(base, input, false, nil)
	if err == nil {
		beyond, err = movesBeyond(base, input, hasRight, allowed)
	}
	return toward, beyond, answered, err
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

// judgedValues is each value that the state at a place, or the state that a right names a place
// with, can hold, by the values of its type (a string) that the judgment tells apart: each state of
// each kind of state (judgedKinds), the empty string, and a word that names no state ("any"). Of a
// place's kind, its own states are the values that an input reads and a right allows there; each
// other value is one that the judgment refuses there (movedBeyond).
func judgedValues(kinds []judgedKind) []string {
	values := []string{"", "any"}
	for _, k := range kinds {
		values = append(values, k.states...)
	}
	slices.Sort(values)
	return slices.Compact(values)
}

// rightsOf is each right at one place of a state of the kind k, by each field of a right at a place
// (placeRight): each form that the judgment has a rule for (rightForms), and for a right that names
// the place, each value that a right can name it with (values, judgedValues).
func rightsOf(k judgedKind, values []string) []placeRight {
	var rights []placeRight
	for _, form := range rightForms() {
		if form != rightNamed {
			rights = append(rights, placeRight{form: form})
			continue
		}
		for _, value := range values {
			rights = append(rights, placeRight{form: form, state: value})
		}
	}
	return rights
}

// answer is one value of the arguments of the judgment of an answer (judgeAnswer): the states of the
// base, whole, and the input's, got, which the domain test hands it as replies of the reader's form
// (replyOf; the base's under one key for each text of whole, the reader's own reply to the base where
// whole is the base as the reader read it, decodedBase); whether the input has a right, hasRight; and
// allowed, each place that its right names with the value that it names there (nil for a right that
// names no place).
type answer struct {
	whole, got, allowed map[string]string
	hasRight            bool
}

// rightOfAnswer is one value of the right of an input as judgeAnswer takes it, by the type of each of
// its two arguments: hasRight, false or true, and named, whether allowed is a map of places (true) or
// nil (false).
type rightOfAnswer struct{ hasRight, named bool }

// answerPlace is the case of one place in an answer that the domain test writes: the state that the
// base reads there, "" where its answer lacks the place; whether the input's answer holds the place
// (held), and the value that it reads there; and, where the right of the answer is a map of places,
// whether it names the place, with value.
type answerPlace struct {
	base, input, value string
	held, named        bool
}

// definedRightAt is the input's right at the place at, as the definition reads it from the approved
// words, apart from the judgment's reading (rightAt): an input with no right has none at any place; a
// right that names no place (allowed nil) covers each place, as a repository path of a tree written as
// one space ends each carry across that tree (R8); and a right that names its places names the place
// at with the one value that allowed gives it there, or leaves it out.
func definedRightAt(hasRight bool, allowed map[string]string, at string) placeRight {
	if !hasRight {
		return placeRight{form: rightNone}
	}
	if allowed == nil {
		return placeRight{form: rightEach}
	}
	if value, named := allowed[at]; named {
		return placeRight{form: rightNamed, state: value}
	}
	return placeRight{form: rightLeftOut}
}

// definedAnswer is the definition (definedBeyond) applied to the answer a place by place: the line of
// each place of a.got that moved toward true with no right, and of each that moved toward true beyond
// a's right there (definedRightAt), as judgeAnswer writes them, in the order of the places; kinds is
// the kind of each place. covered is false where the definition covers no reading of a place of a.got,
// as at a place that a right names with a value that is no state of its kind: the judgment must refuse
// such an answer.
func definedAnswer(a answer, kinds map[string]judgedKind) (toward, beyond []string, covered bool) {
	for _, at := range slices.Sorted(maps.Keys(a.got)) {
		kind := kinds[at].name
		towardHere, coveredToward := definedBeyond(kind, a.whole[at], a.got[at], placeRight{form: rightNone})
		beyondHere, coveredBeyond := definedBeyond(kind, a.whole[at], a.got[at], definedRightAt(a.hasRight, a.allowed, at))
		if !coveredToward || !coveredBeyond {
			return nil, nil, false
		}
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
	return toward, beyond, true
}

// rightWords is the words of the right r of an answer, for a line of a failure.
func rightWords(r rightOfAnswer) string {
	switch {
	case !r.hasRight && !r.named:
		return "no right"
	case !r.hasRight:
		return "no right, with a map of places"
	case !r.named:
		return "a right that names no place"
	}
	return "a right that names its places"
}

// answerCase is the words of the case p of a place of the kind kind under the right r, for a line of
// a failure.
func answerCase(kind string, r rightOfAnswer, p answerPlace) string {
	base, input := "none", "none"
	if p.base != "" {
		base = strconv.Quote(p.base)
	}
	if p.held {
		input = strconv.Quote(p.input)
	}
	words := fmt.Sprintf("%s at %s in the base and %s at the input, %s", kind, base, input, rightWords(r))
	switch {
	case r.named && p.named:
		words += fmt.Sprintf(", which names the place with %q", p.value)
	case r.named:
		words += ", which leaves the place out"
	}
	return words
}

// background is the case of each other place of an answer beside a place on a case of its own (an
// answerPlace of each): as the base reads it, or taken out of the input's answer (out); and, where the
// right of the answer is a map of places, named with the state that the base reads there, or left
// out of it (named).
type background struct{ out, named bool }

// backgroundWords is the words of the background bg under the right r, for a line of a failure.
func backgroundWords(r rightOfAnswer, bg background) string {
	words := "each other place as the base reads it"
	if bg.out {
		words = "each other place taken out of the input's answer"
	}
	switch {
	case r.named && bg.named:
		words += " and named with the base's state"
	case r.named:
		words += " and left out of the right"
	}
	return words
}

// eachAnswer hands judge each answer that the domain test judges of one base, whose reader's answer
// is whole, each place of it of the kind that kinds gives, with the words of its case and where in the
// base it is. Each answer is a value of judgeAnswer's arguments, each ranged over by its type: the
// input's right (rightOfAnswer), hasRight false or true, with allowed nil or a map of places; and at
// each place (answerPlace), the state that the base reads, none or each state of its kind; the
// input's, none (the place taken out of its answer) or each value that the state at a place can hold
// (values, judgedValues: each state of its kind, and each value that is no state of it, the empty
// string, a word and each other kind's states); and, where allowed is a map, the place left out of it
// or named with each of those values. Under each right, at each place:
//   - each case of its kind there (each state that the base reads and none, each value that the input
//     reads and none, and, where the right is a map, the place left out of it or named with each state
//     of its kind), with each other place as the base reads it and, where the right is a map, named
//     with that state;
//   - where the right is a map, each value that is no state of its kind, named at the place, with
//     each value that the input reads there and none, the base's state as it reads it, and each other
//     place as above;
//   - and each state that the base reads and none, with each value that the input reads and none, the
//     place left out of a right that is a map, so that each move there is beyond such a right, beside
//     each other background (background): each other place taken out of the input's answer, and,
//     where the right is a map, each other place left out of it, as the base reads it or taken out.
//
// So each base has answers in which every place is taken out, answers that lack a place of the base
// beside a move toward true, answers whose right names places and none of the answer's, answers
// whose right is a map that names no place, and answers whose input reads, at a place, a value that
// is no state of its kind, beside each case of the base and the right there. And under each right,
// answers that move each place at once: in the answer n, the place i takes the case n+i of its kind,
// so that each place takes each case of its kind beside other places' cases.
func eachAnswer(t *testing.T, whole map[string]string, kinds map[string]judgedKind, values []string, judge func(c, where string, a answer)) {
	t.Helper()
	places := slices.Sorted(maps.Keys(whole))
	var rights []rightOfAnswer
	for _, hasRight := range []bool{false, true} {
		for _, named := range []bool{false, true} {
			rights = append(rights, rightOfAnswer{hasRight: hasRight, named: named})
		}
	}
	// inputs is each case of the input's answer at one place: the place taken out of it, and the place
	// held with each value of values.
	inputs := []answerPlace{{}}
	for _, value := range values {
		inputs = append(inputs, answerPlace{held: true, input: value})
	}
	// casesOf is each case of a place of the kind k under the right r: each state that the base reads
	// and none, each case of the input there (inputs), and, where the right is a map, the place left
	// out of it or named with each state of k.
	casesOf := func(k judgedKind, r rightOfAnswer) []answerPlace {
		entries := []answerPlace{{}}
		if r.named {
			for _, state := range k.states {
				entries = append(entries, answerPlace{named: true, value: state})
			}
		}
		var cases []answerPlace
		for _, base := range append([]string{""}, k.states...) {
			for _, in := range inputs {
				for _, e := range entries {
					cases = append(cases, answerPlace{base: base, input: in.input, held: in.held, value: e.value, named: e.named})
				}
			}
		}
		return cases
	}
	// write hands judge the answer of the base under the right r with each place on its case of cases.
	write := func(c, where string, r rightOfAnswer, cases map[string]answerPlace) {
		a := answer{whole: maps.Clone(whole), got: maps.Clone(whole), hasRight: r.hasRight}
		if r.named {
			a.allowed = map[string]string{}
		}
		for _, at := range places {
			p := cases[at]
			if p.base == "" {
				delete(a.whole, at)
			} else {
				a.whole[at] = p.base
			}
			if p.held {
				a.got[at] = p.input
			} else {
				delete(a.got, at)
			}
			if r.named && p.named {
				a.allowed[at] = p.value
			}
		}
		judge(c, where, a)
	}
	// each writes, for each case of cases, the answer with the place at on that case and each other
	// place on the background bg, under the right r.
	each := func(r rightOfAnswer, bg background, at string, cases []answerPlace) {
		for _, focus := range cases {
			all := make(map[string]answerPlace, len(places))
			for _, other := range places {
				all[other] = answerPlace{base: whole[other], input: whole[other], held: !bg.out, value: whole[other], named: bg.named}
			}
			all[at] = focus
			write(answerCase(kinds[at].name, r, focus)+"; "+backgroundWords(r, bg), "at "+at, r, all)
		}
	}
	for _, r := range rights {
		backgrounds := []background{{out: true}}
		if r.named {
			backgrounds = []background{{}, {out: true}, {out: true, named: true}}
		}
		for _, at := range places {
			k := kinds[at]
			states := append([]string{""}, k.states...)
			each(r, background{named: r.named}, at, casesOf(k, r))
			if r.named {
				var off []answerPlace
				for _, in := range inputs {
					for _, value := range values {
						if !slices.Contains(k.states, value) {
							off = append(off, answerPlace{base: whole[at], input: in.input, held: in.held, value: value, named: true})
						}
					}
				}
				each(r, background{named: true}, at, off)
			}
			var moves []answerPlace
			for _, base := range states {
				for _, in := range inputs {
					moves = append(moves, answerPlace{base: base, input: in.input, held: in.held})
				}
			}
			for _, bg := range backgrounds {
				each(r, bg, at, moves)
			}
		}
	}
	for _, r := range rights {
		of, rounds := map[string][]answerPlace{}, 0
		for _, at := range places {
			of[at] = casesOf(kinds[at], r)
			rounds = max(rounds, len(of[at]))
		}
		for n := range rounds {
			cases := make(map[string]answerPlace, len(places))
			for i, at := range places {
				cases[at] = of[at][(n+i)%len(of[at])]
			}
			write("answers that move each place at once, with "+rightWords(r), fmt.Sprintf("answer %d", n), r, cases)
		}
	}
}

// declaredStates is the value of each constant of the block of the Go file file, a path under this
// package, whose doc comment is doc: the states of a kind as the reader's code declares them, read
// from the file as the build of the reader's binary reads it (sweepReaderCommand, Source), through
// the overlay that the build reads, which ENVOY_TEST_GO_OVERLAY, the command's flags or GOFLAGS gives.
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
// base reads and none, each value that the input reads, and each value of each field of a right at the
// place (placeRight): each form of right that the judgment has a rule for (rightForms, rightRules),
// and for a right that names the place, each value that it can name it with; the input's value and
// the right's each range over each value that the state at a place can hold (judgedValues: each state
// of each kind, the empty string and a word that names no state). Where each is a state of the kind,
// it fails where the definition does not cover the case, where the judgment refuses it, and where the
// two disagree, by the case; where either is no state of the kind, where the judgment reads it or the
// definition covers it. So a state, a kind or a form of right that the code gains with no word of the
// definition for it fails by name. Outside the domain, the judgment refuses a state of no kind at the
// base, a place of no kind and a form of right that it has no rule for. And it reads each base of the
// sweeps (sweepBases) in the reader's process, and decodes the states of each reply itself, apart from
// the decoder that the judgment reads replies with, and fails a base whose reply that decoder reads
// otherwise (decodedBase): each place of each base is of a kind and each state
// that it reads is a state of that kind; and it hands judgeAnswer answers of each base whose arguments
// it ranges over by their types (eachAnswer): the input's right, whether there is one and allowed nil
// or a map; and at each place, the base's state, none or each state of its kind, the input's, none or
// each value of judgedValues, and the value that a map names the place with, or none; the base's
// answer and the input's go to judgeAnswer as replies of the reader's form (replyOf), with the text
// that the reader's process writes for those states, which decodedBase holds to the reader's reply at
// each base, byte for byte. Each base reply of one text goes to judgeAnswer under one key for all of
// its answers, as each sweep hands one base reply, under the key of the base's line, for every input of
// that base: the reader's own reply to the base, under the key of its line (decodedBase), where the
// base is the base as the reader read it, and else the first reply that replyOf gave that text, whose
// key is of random bytes (replyText), which no code can tell from the key of the line that a reply of
// the reader answers; each input's reply has a key of random bytes of its own, as each input of a sweep
// is a line of its own. It holds what judgeAnswer reports of each to the definition applied place by
// place (definedAnswer), and an answer that the definition covers no reading of to the judgment's
// refusal, as it holds a reply that gives no states (the reader's refusal, or a line of no form of the
// reader's) at the base or the input to it. So a judgment of an answer that reads an answer by its
// shape (a place that it lacks or holds, a right that names no place of it, or none at all), passes
// over a place whose input reads a value that is no state of its kind, or reads another right at a
// place than the input's, fails by the case, and so does one that tells the replies that it reads
// apart by their keys (whether they have one, how it relates to their text, or whether an earlier
// call handed it the same key, or the same key and text), or by the members or the order of their
// text. It fails by name where a right at a place (placeRight) or a reply of the reader (readerReply)
// gains a field, or judgeAnswer an argument or a type of one, that it does not range over. It does not
// show what sweepAgainst hands judgeAnswer: that each of the two replies that it hands it for an
// input, the base's and the input's, is the reader's reply to the line that it stands for is the
// sweeps' digests' (judgeAnswer gives back the keys of both, by which the sweep counts the input), and
// that it hands it the input's right is the known positives' (knownPositives), at their bases. Nor does
// it show a judgment that keeps other state across its calls than the keys and texts that it was
// handed, such as the order of its calls or how many calls handed it one key: the domain test hands
// each base's key in an order and a number of its own, which are not a sweep's, so such a judgment
// could tell the two apart.
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
	// The domain ranges over each field of a right at a place and each argument of judgeAnswer by its
	// type: a field or an argument that the code gains, or a type that one of them takes, which the
	// generation does not range over, fails by name.
	for _, f := range reflect.VisibleFields(reflect.TypeFor[placeRight]()) {
		if f.Name != "form" && f.Name != "state" {
			t.Errorf("a right at a place (placeRight) holds the field %s, which the domain test does not range over (rightsOf)", f.Name)
		}
	}
	stateMap, reply := reflect.TypeFor[map[string]string](), reflect.TypeFor[readerReply]()
	// A reply of the reader holds the key of the line that it answers and its text. The domain test
	// writes the text as the reader's process gives it (replyOf, decodedBase), and the key of a base's
	// reply as the reader's process gives it for the base as read (decodedBase), and else a key of
	// random bytes (replyText), one for each text of a base's reply and a new one for each input's
	// reply, which no code can tell from a line's key: a field that a reply gains, which it does not
	// write, fails by name.
	for _, f := range reflect.VisibleFields(reply) {
		if f.Name != "line" && f.Name != "text" {
			t.Errorf("a reply of the reader (readerReply) holds the field %s, which the domain test does not write as the reader's process does (replyOf)", f.Name)
		}
	}
	ranged := []reflect.Type{reply, reply, reflect.TypeFor[bool](), stateMap}
	if judge := reflect.TypeOf(judgeAnswer); judge.NumIn() != len(ranged) {
		t.Errorf("the judgment of an answer (judgeAnswer) takes %d arguments, and the domain test ranges over %d (eachAnswer)", judge.NumIn(), len(ranged))
	} else {
		for i, want := range ranged {
			if judge.In(i) != want {
				t.Errorf("the judgment of an answer (judgeAnswer) takes a %s as its argument %d, and the domain test ranges over a %s there (eachAnswer)", judge.In(i), i+1, want)
			}
		}
	}
	values := judgedValues(kinds)
	cases := 0
	for _, k := range kinds {
		for _, base := range append([]string{""}, k.states...) {
			for _, input := range values {
				for _, right := range rightsOf(k, values) {
					cases++
					got, err := movedBeyond(k, base, input, right)
					want, covered := definedBeyond(k.name, base, input, right)
					switch {
					case !slices.Contains(k.states, input) || right.form == rightNamed && !slices.Contains(k.states, right.state):
						if err == nil {
							t.Errorf("%s: the judgment (movedBeyond) reads it", cell(k.name, base, input, right))
						}
						if covered {
							t.Errorf("%s: the definition (definedBeyond) covers it", cell(k.name, base, input, right))
						}
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
		{"a base that reads a state of no kind", stateCheck, statementMet, checkTrue, placeRight{form: rightNone}},
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
	empty := replyOf(t, map[string]string{})
	refusal := replyText(`{"refused":true,"refusal":"the derivation refuses the scenario"}` + "\n")
	for _, out := range []struct {
		what       string
		whole, got readerReply
	}{
		{"a place of no kind", empty, replyOf(t, map[string]string{"subject 1 note of c": checkTrue})},
		{"the reader's refusal as the input's reply", empty, refusal},
		{"the reader's refusal as the base's reply", refusal, empty},
		{"a line of no form of the reader's as the input's reply", empty, replyText(`{"states":{},"moved":true}` + "\n")},
		{"a line of no form of the reader's as the base's reply", replyText("{\n"), empty},
	} {
		if _, _, _, err := judgeAnswer(out.whole, out.got, false, nil); err == nil {
			t.Errorf("%s: the judgment of an answer (judgeAnswer) reads it", out.what)
		}
	}
	bases := sweepBases(t)
	p := newSweepReader(t).start(t)
	// Each base reply of one text goes to judgeAnswer under one key for all of its answers, as each
	// sweep hands one base reply, under the key of the base's line, for every input of that base: the
	// reader's own reply to each base (decodedBase), and for each other text the first reply that
	// replyOf gave it (baseReply).
	wholes := map[string]map[string]string{}
	baseReplies := map[string]readerReply{}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		whole, reply := decodedBase(t, p, name, bases[name])
		wholes[name], baseReplies[reply.text] = whole, reply
	}
	baseReply := func(states map[string]string) readerReply {
		r := replyOf(t, states)
		if kept, held := baseReplies[r.text]; held {
			return kept
		}
		baseReplies[r.text] = r
		return r
	}
	places, answers := 0, 0
	disagree := map[string][]string{}
	held := map[string]bool{} // the form of each right at a place of an answer, as the definition reads it
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		whole := wholes[name]
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
		eachAnswer(t, whole, kindOf, values, func(c, where string, a answer) {
			answers++
			for at := range a.got {
				held[definedRightAt(a.hasRight, a.allowed, at).form] = true
			}
			wantToward, wantBeyond, covered := definedAnswer(a, kindOf)
			toward, beyond, _, err := judgeAnswer(baseReply(a.whole), replyOf(t, a.got), a.hasRight, a.allowed)
			switch {
			case !covered && err == nil:
				disagree[c] = append(disagree[c], fmt.Sprintf("%s, %s: the judgment gives %q toward true and %q beyond the right, and the definition covers no reading of it", name, where, toward, beyond))
			case covered && (err != nil || !slices.Equal(toward, wantToward) || !slices.Equal(beyond, wantBeyond)):
				disagree[c] = append(disagree[c], fmt.Sprintf("%s, %s: the judgment gives %q toward true and %q beyond the right (%v), and the definition %q and %q", name, where, toward, beyond, err, wantToward, wantBeyond))
			}
		})
	}
	// Each form of the judgment's rules is the form of a right at a place of an answer that the test
	// writes, and each such form is one that the judgment has a rule for.
	for _, form := range rightForms() {
		if !held[form] {
			t.Errorf("the judgment has a rule for the form %q, which no right at a place of an answer that the domain test writes holds (eachAnswer, definedRightAt)", form)
		}
	}
	for _, form := range slices.Sorted(maps.Keys(held)) {
		if !slices.Contains(rightForms(), form) {
			t.Errorf("the domain test writes answers whose rights hold the form %q at a place, which the judgment has no rule for (rightRules)", form)
		}
	}
	for _, c := range slices.Sorted(maps.Keys(disagree)) {
		t.Errorf("%s: the judgment of an answer (judgeAnswer) disagrees with the definition applied place by place (definedAnswer) in %d answers, the first %s", c, len(disagree[c]), disagree[c][0])
	}
	t.Logf("%d cases of %d kinds of state; %d places of %d bases, %d whole answers judged", cases, len(kinds), places, len(bases), answers)
}

// decodedBase is the states of the base fsys, of the name name, as the reader's process p writes them
// in its reply, decoded by the domain test itself, and that reply: the reply's member "states" as
// encoding/json/v2 reads it, apart from the decoder that the judgment reads each reply with
// (readerReply.states, through readerReply.answer, the one reading of the reader's lines). It is the
// domain test's oracle of a reply, a reading of its own on purpose, as the definition (definedAnswer)
// is of the judgment. It fails the test t, by the base's name, where that decoder reads the same reply
// otherwise. So the places of the answers that the domain test writes (eachAnswer) are the places that
// the reader wrote, and a fault of the judgment's decoder that drops or changes places of a reply fails
// here by the base's name, and by the answers that move those places, rather than taking the places
// out of the test's domain and the judgment's reading alike. It fails t by the base's name, too, where
// the reply that the domain test writes of those states (replyOf) is not the reader's reply, byte for
// byte: so the replies that the domain test hands judgeAnswer are of the reader's form. The reply that
// it gives back is the one that ask returned, under the key of the base's line, the very reply that
// each sweep hands judgeAnswer as the base's for each input of that base; the domain test hands it as
// the base's reply of each answer whose base has its text, which is each answer whose base is the base
// as the reader read it.
func decodedBase(t *testing.T, p *readerProcess, name string, fsys fstest.MapFS) (map[string]string, readerReply) {
	t.Helper()
	line, _ := readerLine(t, fsys)
	reply, err := p.ask(t, line)
	if err != nil {
		t.Fatalf("%s: the base: %v", name, err)
	}
	var own struct {
		States map[string]string `json:"states"`
	}
	if err := json.Unmarshal([]byte(reply.text), &own); err != nil {
		t.Fatalf("%s: the base's reply %q: %v", name, reply.text, err)
	}
	if read, err := reply.states(); err != nil || !maps.Equal(read, own.States) {
		t.Errorf("%s: the judgment's decoder (readerReply.states) reads the base's reply %q as %q (%v), where its member \"states\" is %q", name, reply.text, read, err, own.States)
	}
	if mine := replyOf(t, own.States); mine.text != reply.text {
		t.Errorf("%s: the domain test writes the base's states as the reply %q (replyOf), and the reader's process wrote %q", name, mine.text, reply.text)
	}
	return own.States, reply
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
