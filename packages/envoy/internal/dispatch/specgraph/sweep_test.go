package specgraph

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// sweepMaps is each object of the JSON files of a scenario whose names its writer picks, as the
// forms write them ({"<repository path>":"<content label>"}, {"<commit>":{"<check id>":…}}), by its
// place: the names of the path to it from the root of the file, "*" for any name or index. A member
// of such an object is an entry of a map: a record of the scenario, which the sweep puts null in the
// place of and TestTakingOutAWholeRecordMovesAStateOnlyByItsRight takes out, and whose key the
// sweep writes as a name that names nothing. Like each table that a sweep reads, it is a function
// that gives a new value at each call, not a variable of the package.
func sweepMaps() map[string][][]string {
	return map[string][][]string{
		"history.json": {{"commits", "*", "files"}},
		"ties.json":    {{}, {"*"}},
		"uncited.json": {{}, {"*"}},
	}
}

// sweepPaths is each place of a repository path in the JSON files of a scenario: the keys of a tree
// of the history, the entries of a tie, and the paths of a fault of a result. The sweep writes each
// such path in the forms that name no repository path, which no tie entry covers.
func sweepPaths() map[string]struct{ keysOf, values [][]string } {
	return map[string]struct{ keysOf, values [][]string }{
		"history.json": {keysOf: [][]string{{"commits", "*", "files"}}},
		"ties.json":    {values: [][]string{{"*", "*", "*"}}},
		"results.json": {values: [][]string{{"*", "fault_paths", "*"}}},
	}
}

// namesOfNothing are the texts that the sweep writes at each string place and as each key of a map:
// no name, and a name of no letter and no digit.
func namesOfNothing() []string { return []string{"", " "} }

// notRepoPaths are the forms of path that name no repository path, as the sweep writes them.
func notRepoPaths(path string) []string {
	forms := []string{"./" + path, "/" + path, path + "/"}
	if strings.Contains(path, "/") {
		forms = append(forms, strings.Replace(path, "/", "//", 1))
	}
	return forms
}

// An input that says nothing at one place reads as the zero value of that place, a real value, and
// a rule that read it as one would turn a state toward true. So the sweep reads each JSON file of each
// scenario of testdata/scenarios as a JSON tree, with no Go type, and derives the state of each input
// that says nothing at one place: each member of each object of the format left out, null at each
// place (a member, an entry of a map, an element of a list, the whole file), each file of the
// scenario taken out; each name that names nothing: "" and " " at each string place and as each
// key of each map of the format (sweepMaps), and each repository path of a tree, a tie or a fault
// written as "./<path>", "/<path>", "<path>/" and with a "/" doubled (sweepPaths). No such input
// may move a check toward true, a statement or its own state toward met, or a wait toward ends,
// against the state of the whole scenario: a member with a mark of zero is no exception, and a mark
// whose reading moves a state toward true is wrong. The reader may refuse the input. A record taken
// out whole, such as a result, is another input, not one that says nothing
// (TestTakingOutAWholeRecordMovesAStateOnlyByItsRight), and so is a named leg written where a
// record has "": it states that the job had legs there, which an older result of that leg answers
// by right (R8). The scenarios hold each shape that a rule reads: a check of the code with a matrix
// and a silent leg, its failing example planted on a commit (planted_on, fault_paths), a wait on
// its statement, a failure of production at the at of its pass, a refusal of CI, and, in
// production-and-graph, a program check of production whose runs name the commit of their test
// (test_commit), a query of the graph at the transaction of its subject (graph_tx), and a run of a
// job that has not ended. So each member that a rule reads is in a record of some scenario, and
// each place of it is swept. The sweep also reads the shapes that no scenario holds (sweepBases).
// Each sweep fails an input whose files are its base's (sweepAgainst), so that a writer that writes
// nothing fails by name, and each sweep reads back each write that it makes against the write that
// it means (filesRead): each file by its name, with each other file of the base unchanged and no
// file added; a JSON file as the tree of the base's file with the one change that the input names,
// compared whole (changedFile); a text file by its bytes (filesWritten); and a refusal of CI as the
// reader reads it (refusalRead). The sweep builds what it means before its writer runs, from a
// decode of the base of its own; a writer is a function of the package, which gets a tree or files
// of its own and copies of the place and the value (changedFile, filesWritten). So a writer that
// writes another value, writes it at another place, under another key or under another file name,
// or changes another place of the file too, fails by name. Each sweep reads each input in the
// reader's own process (sweepReader), which no code of the test process can reach, and derives at
// least its floors of inputs of each base, of those that the reader reads and of those that it
// refuses (sweepFloors), so that a fault that drops inputs, a sweep that does not derive an input
// that it built, and a fault that moves inputs from one kind to the other fail by name too. One
// right, which sweepInputs gives the input where it writes it: a repository path of a tree written
// " " is a real path, since git holds a file of that name (git ls-tree of a tree that holds one
// lists the path " "), so the tree changed at a path that a tie covers, and a result there, a
// failure among them, carries no more across it (R8: "A result at one commit also counts at another
// commit when no code that the check runs changed between the two"); a base whose failure at one
// commit carries to another shows it. A path of a fault written " " is no path, as one of only
// white space is none, so its record does not say what its fault changed, and as the newest failing
// example it reads unknown, no_fault_paths where it was the one path and blank_fault_path beside a
// real one: it has no right, and a base whose failing example of two paths that missed its fault is
// newer than a caught one shows that it needs none. A content label is no path, and one of " "
// names no content, so the reader refuses it and it has no right.
func TestNoInputThatSaysNothingMovesAStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	floorsCover(t, "tree", bases)
	reader := newSweepReader(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			p := reader.start(t)
			inputs, roundTrip := sweepInputs(t, base)
			if got, err := p.states(t, roundTrip); err != nil || !maps.Equal(got, baseStates(t, p, base)) {
				t.Fatalf("each JSON file of the scenario written again by the sweep does not read as the scenario: %v", err)
			}
			n := sweepAgainst(t, p, name, base, inputs)
			atFloor(t, "tree", name, n)
			legs := 0 // the inputs that write the leg of no matrix at the leg of a record
			for _, in := range inputs {
				if strings.HasSuffix(in.what, `/leg as ""`) {
					legs++
				}
			}
			t.Logf(`%d inputs: %d refused, %d read as the whole scenario, %d read another state, %d of them toward true by right; %d of them a leg written as ""`,
				n.inputs, n.refused, n.same, n.moved, n.byRight, legs)
		})
	}
}

// baseStates is the state of each check, statement and wait of the base, as the reader's process p
// reads it.
func baseStates(t *testing.T, p *readerProcess, base fstest.MapFS) map[string]string {
	t.Helper()
	whole, err := p.states(t, base)
	if err != nil {
		t.Fatalf("the base: %v", err)
	}
	return whole
}

// sweepCount is what a sweep read: the inputs that it built, and of those that sweepAgainst judged,
// comparing each one's states with what the sweep wants of them, how many the reader or the
// derivation refused, how many read as the base, how many read another state with no state moved
// toward true or with a state moved by the input's right, how many of those moved a state toward
// true by a right, with what each of them changed; and judged, the key of the base's line, the bytes
// that the reader's process read of the base (readerLine), then each judged input's key, kind ('r'
// read, 'x' refused), right and allowed places (counted), in the order of the inputs. An input that
// fails the comparison fails the test and is counted nowhere. So the floors (atFloor), which count
// the inputs judged and hold the digest of judged, fail by name where the sweep skips an input that
// it built, drops one that it judged, writes an input's bytes otherwise, reads a base of other bytes,
// or judges an input by another right. known holds the lines that the sweep reported of each of its
// known positives (knownPositives), by its words, which no floor counts: a sweep whose judgment no
// longer reports one move of the table of moves (knownMoves), a kind of state moved toward true with
// no right or beyond a right at one kind of place, whether it skips that judgment, counts such an
// input as read or computes it blind, fails by the name of the known positive that makes that move
// (sweepAgainst).
type sweepCount struct {
	inputs, refused, same, moved, byRight int
	rights                                []string
	judged                                []byte
	known                                 map[string][]string
}

// counted counts the judged input in, whose key is key, as of the kind kind, and is whether it
// counted it: it counts no known positive, which the floors do not hold. It appends to judged the
// key, the kind, the input's right, and each place that the right allows with the state that it
// allows there, in the order of the places (nil, a right that covers each place, written apart from
// a right that allows no place), so that a right that any code gives an input, takes from it, widens
// or narrows moves the digest of its base, and fails by name (atFloor).
func (n *sweepCount) counted(in sweepInput, key [sha256.Size]byte, kind byte) bool {
	if in.known {
		return false
	}
	n.judged = append(append(n.judged, key[:]...), kind)
	n.judged = fmt.Appendf(n.judged, "right %q", in.right)
	if in.allowed == nil {
		n.judged = append(n.judged, " allows each place;"...)
		return true
	}
	n.judged = fmt.Appendf(n.judged, " allows %d places:", len(in.allowed))
	for _, at := range slices.Sorted(maps.Keys(in.allowed)) {
		n.judged = fmt.Appendf(n.judged, " %q %q", at, in.allowed[at])
	}
	n.judged = append(n.judged, ';')
	return true
}

// report fails the test by failure, the line of the input in that moves the states moved toward
// true with no right or beyond its right; or, where in is a known positive, keeps moved as what the
// sweep reported of it.
func (n *sweepCount) report(t *testing.T, in sweepInput, moved []string, failure string) {
	t.Helper()
	if in.known {
		n.known[in.what] = moved
		return
	}
	t.Error(failure)
}

// knownRight is the right of each known positive that a sweep must report beyond its right
// (knownPositives): it holds at each place the state that the base reads there, but at places that
// the input moves, which it may leave out or name with a state that is neither the base's nor the
// input's; so each move of the input is beyond it, at a place that the right leaves out, at one that
// it names with the base's state, or at one that it names with another state.
const knownRight = "a right that holds at each place the state that the base reads there, but at places that the input moves, which it may leave out or name with a state that is neither the base's nor the input's, so that each move is beyond it"

// The words of the known positives (knownPositives), by which the table of moves names each
// (knownMoves).
const (
	knownNoRight    = "the known positive with no right, results.json /5 (a failure of production-delivery-takes-under-a-second) taken out"
	knownBeyond     = "the known positive beyond its right, results.json /5 (a failure of production-delivery-takes-under-a-second) with the outcome pass"
	knownWait       = "the known positive of a wait, results.json /4 (the failing run of a-restart-keeps-the-message) with the outcome pass"
	knownOutOrOther = "the known positive beyond its right at places that it leaves out or names with another state, jobs.json /1 (the leg arm of go-checks at m2) taken out"
	knownBaseState  = "the known positive beyond its right at places that it names with the base's state, results.json /2 (a pass of a-restart-loses-no-message at c1) on the leg arm"
)

// knownPositives is each input that a sweep must report at the base name, with moves, the lines of
// the states that it is built to move toward true, as towardTrue writes them, in their order: each
// move of the table of moves (knownMoves), a kind of state moved with no right or beyond a right at a
// kind of place, is a move of one of them. At silent-leg, its failing result of
// production-delivery-takes-under-a-second (results.json /5) taken out, with no right: the check
// reads true at the third subject, where silent-leg reads it false (a row of
// TestTakingOutAWholeRecordMovesAStateOnlyByItsRight), and its statement delivery-is-fast and that
// statement's own state read met; and that result with the outcome pass, with the right knownRight,
// whose places leave out that check's place at the third subject and hold the base's state at each
// other place: so it moves the same states, each beyond its right, the check at a place that the
// right leaves out, and the statement and its own state at places that the right names with the
// base's state. At wait-cycle, the failing run of a-restart-keeps-the-message (results.json /4) with
// the outcome pass, with no right: that check reads true, a-message-survives-a-restart, its own state
// and envoy-delivers-messages read met, and the wait of u-b on a-message-survives-a-restart ends. And
// at silent-leg, two inputs that each end the silent leg of go-checks at m2 and move the same nine
// states (legMoves): the check a-restart-loses-no-message, which silent-leg reads unknown at the first
// and third subjects, reads true there, a-stored-message-survives-a-restart, its own state and
// messages-reach-sessions read met there, and the wait of u-a on a-stored-message-survives-a-restart
// ends at the first subject. One takes out the leg arm of go-checks at m2 (jobs.json /1), which ended
// with no result of the check, with the right knownRight, which leaves out each place that the input
// moves at the first subject and names each that it moves at the third with a state that is neither
// the base's nor the input's (false for the check, not_met for the statements and the own state): so
// it moves a check, a statement, an own state and a wait beyond its right at places that the right
// leaves out, and a check, a statement and an own state at places that it names with another state.
// The other writes the second pass of the check at c1 (results.json /2), on the leg linux, on the
// leg arm, so that the leg arm has a pass that carries to m2, with the right knownRight that holds
// the base's state at each place: so it moves each of the nine at a place that the right names with
// the base's state. whole is the states of the base.
func knownPositives(t *testing.T, name string, base fstest.MapFS, whole map[string]string) []sweepInput {
	t.Helper()
	passed := func(i int) treeChange { return treeChange{place: []string{strconv.Itoa(i), "outcome"}, value: "pass"} }
	switch name {
	case "silent-leg":
		moves := []string{
			`subject 3 check production-delivery-takes-under-a-second: "true", and "false" in the whole scenario`,
			`subject 3 own state of delivery-is-fast: "met", and "not_met" in the whole scenario`,
			`subject 3 statement delivery-is-fast: "met", and "not_met" in the whole scenario`,
		}
		checkLeftOut := maps.Clone(whole)
		delete(checkLeftOut, "subject 3 check production-delivery-takes-under-a-second")
		legMoves := []string{
			`subject 1 check a-restart-loses-no-message: "true", and "unknown" in the whole scenario`,
			`subject 1 own state of a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
			`subject 1 statement a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
			`subject 1 statement messages-reach-sessions: "met", and "unknown" in the whole scenario`,
			`subject 1 wait of u-a on a-stored-message-survives-a-restart: "ends", and "holds" in the whole scenario`,
			`subject 3 check a-restart-loses-no-message: "true", and "unknown" in the whole scenario`,
			`subject 3 own state of a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
			`subject 3 statement a-stored-message-survives-a-restart: "met", and "unknown" in the whole scenario`,
			`subject 3 statement messages-reach-sessions: "met", and "unknown" in the whole scenario`,
		}
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
			what:  knownNoRight,
			fsys:  changedFile(t, base, "results.json", treeChange{place: []string{"5"}, out: true}, leftOutAt),
			known: true, moves: moves,
		}, {
			what:  knownBeyond,
			fsys:  changedFile(t, base, "results.json", passed(5), valueAt),
			right: knownRight, allowed: checkLeftOut, known: true, moves: moves,
		}, {
			what:  knownOutOrOther,
			fsys:  changedFile(t, base, "jobs.json", treeChange{place: []string{"1"}, out: true}, leftOutAt),
			right: knownRight, allowed: outOrOther, known: true, moves: legMoves,
		}, {
			what:  knownBaseState,
			fsys:  changedFile(t, base, "results.json", treeChange{place: []string{"2", "leg"}, value: "arm"}, valueAt),
			right: knownRight, allowed: maps.Clone(whole), known: true, moves: legMoves,
		}}
	case "wait-cycle":
		return []sweepInput{{
			what:  knownWait,
			fsys:  changedFile(t, base, "results.json", passed(4), valueAt),
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

// The kinds of state whose move toward true the judgment reports, by the form in which the reader
// writes the place of each (sweepreader's sweepStates, stateKind): "subject <n> check <id>",
// "subject <n> statement <id>", "subject <n> own state of <id>" and "subject <n> wait of <unit> on
// <id>". A statement and its own state are two places, so a judgment can be blind to one alone.
const (
	stateCheck     = "a check"
	stateStatement = "a statement"
	stateOwn       = "a statement's own state"
	stateWait      = "a wait"
)

// The kinds of place of a move toward true that the judgment reports (sweepAgainst, placeKind): the
// move of an input with no right; and beyond a right that names its places, a move at a place that
// the right leaves out, at one that it names with the state that the base reads there, and at one
// that it names with another state, past which the move goes to a third.
const (
	placeNoRight = "with no right"
	placeLeftOut = "at a place that the right leaves out"
	placeBase    = "at a place that the right names with the base's state"
	placeOther   = "at a place that the right names with another state"
)

// stateKinds and placeKinds are each kind of state and each kind of place of the table of moves
// (knownMoves).
func stateKinds() []string { return []string{stateCheck, stateStatement, stateOwn, stateWait} }
func placeKinds() []string { return []string{placeNoRight, placeLeftOut, placeBase, placeOther} }

// stateKind is the kind of the state at the place at, by the form of the place, or "" for a place of
// no kind of the table of moves.
func stateKind(at string) string {
	words := strings.SplitN(at, " ", 3)
	if len(words) != 3 || words[0] != "subject" {
		return ""
	}
	if _, err := strconv.Atoi(words[1]); err != nil {
		return ""
	}
	for _, kind := range []struct{ prefix, kind string }{{"check ", stateCheck}, {"statement ", stateStatement}, {"own state of ", stateOwn}, {"wait of ", stateWait}} {
		if strings.HasPrefix(words[2], kind.prefix) {
			return kind.kind
		}
	}
	return ""
}

// placeKind is the kind of place of the move of the input in at the place at, where whole is the
// states of the base: with no right where in has none, else by what in's right holds at that place.
func placeKind(in sweepInput, whole map[string]string, at string) string {
	if in.right == "" {
		return placeNoRight
	}
	switch allowed, named := in.allowed[at]; {
	case !named:
		return placeLeftOut
	case allowed == whole[at]:
		return placeBase
	}
	return placeOther
}

// knownMove is one row of the table of moves (knownMoves): a kind of state and a kind of place, and
// the known positive that makes that move, by its base and its words; or, in none, why no input can
// make it.
type knownMove struct {
	state, place, base, known, none string
}

// knownMoves is the table of the moves that the judgment reports, one row for each kind of state at
// each kind of place, each with the known positive (knownPositives) that makes the move, so that a
// judgment blind to that one move fails by the name of that known positive (sweepAgainst). One move
// can occur at no place: a wait reads holds or ends and nothing else (sweepreader's sweepStates), and
// a move toward true reads ends, so a right that names a wait's place with a state other than the
// base's, holds, names it with ends, and the move is within it.
// TestEachMoveThatTheJudgmentReportsHasAKnownPositive holds the table to the known positives.
func knownMoves() []knownMove {
	const waitOfTwoStates = "a wait reads holds or ends and nothing else, and a move toward true reads ends: a right that names a wait's place with a state other than the base's, holds, names it with ends, so the move is within the right"
	return []knownMove{
		{state: stateCheck, place: placeNoRight, base: "silent-leg", known: knownNoRight},
		{state: stateStatement, place: placeNoRight, base: "silent-leg", known: knownNoRight},
		{state: stateOwn, place: placeNoRight, base: "silent-leg", known: knownNoRight},
		{state: stateWait, place: placeNoRight, base: "wait-cycle", known: knownWait},
		{state: stateCheck, place: placeLeftOut, base: "silent-leg", known: knownBeyond},
		{state: stateStatement, place: placeLeftOut, base: "silent-leg", known: knownOutOrOther},
		{state: stateOwn, place: placeLeftOut, base: "silent-leg", known: knownOutOrOther},
		{state: stateWait, place: placeLeftOut, base: "silent-leg", known: knownOutOrOther},
		{state: stateCheck, place: placeBase, base: "silent-leg", known: knownBaseState},
		{state: stateStatement, place: placeBase, base: "silent-leg", known: knownBeyond},
		{state: stateOwn, place: placeBase, base: "silent-leg", known: knownBeyond},
		{state: stateWait, place: placeBase, base: "silent-leg", known: knownBaseState},
		{state: stateCheck, place: placeOther, base: "silent-leg", known: knownOutOrOther},
		{state: stateStatement, place: placeOther, base: "silent-leg", known: knownOutOrOther},
		{state: stateOwn, place: placeOther, base: "silent-leg", known: knownOutOrOther},
		{state: stateWait, place: placeOther, none: waitOfTwoStates},
	}
}

// TestEachMoveThatTheJudgmentReportsHasAKnownPositive holds the table of moves (knownMoves) to the
// known positives (knownPositives), which it reads at each base of the sweeps (sweepBases) with the
// base's states as the reader's process reads them: the table has one row for each kind of state at
// each kind of place (stateKinds, placeKinds) and no other; the place of each state of each base is of
// a kind of the table, and each wait reads holds or ends, as the row of a move that no input can make
// says; each known positive with a right names its places; each line of each known positive's moves
// is a move toward true of the base's state at its place (towardTrue), beyond the right where the
// input has one; each row's known positive makes that row's move, by its moves and its right
// (placeKind); and no known positive makes the move of a row that says why none can. So a kind of
// state or of place added with no known positive that makes it, a known positive taken out, and a
// right changed so that its known positive no longer makes its row's move each fail by name. It logs
// each move with the known positives that make it.
func TestEachMoveThatTheJudgmentReportsHasAKnownPositive(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	p := newSweepReader(t).start(t)
	made := map[[2]string][]string{}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		base := bases[name]
		whole := baseStates(t, p, base)
		for _, at := range slices.Sorted(maps.Keys(whole)) {
			switch kind := stateKind(at); {
			case kind == "":
				t.Errorf("%s: the place %q is of no kind of state of the table of moves (knownMoves)", name, at)
			case kind == stateWait && whole[at] != "holds" && whole[at] != "ends":
				t.Errorf("%s: the wait %q reads %q, and the table of moves (knownMoves) holds that a wait reads holds or ends", name, at, whole[at])
			}
		}
		for _, k := range knownPositives(t, name, base, whole) {
			if k.right != "" && k.allowed == nil {
				t.Errorf("%s, %s: the known positive has a right that names no place, so the judgment reports no move of it (knownMoves)", name, k.what)
				continue
			}
			for _, line := range k.moves {
				at, rest, _ := strings.Cut(line, ": ")
				got, err := strconv.QuotedPrefix(rest)
				if err == nil {
					got, err = strconv.Unquote(got)
				}
				switch {
				case err != nil || stateKind(at) == "" || !slices.Equal(towardTrue(whole, map[string]string{at: got}), []string{line}):
					t.Errorf("%s, %s: the line %q of its moves is no move toward true of the base's state at a place of a kind of the table of moves (knownMoves)", name, k.what, line)
					continue
				case k.right != "" && k.allowed[at] == got:
					t.Errorf("%s, %s: its right allows %q at %s, so the move of the line %q is within its right", name, k.what, got, at, line)
					continue
				}
				move := [2]string{stateKind(at), placeKind(k, whole, at)}
				made[move] = append(made[move], name+": "+k.what)
			}
		}
	}
	rows := map[[2]string]bool{}
	for _, row := range knownMoves() {
		move := [2]string{row.state, row.place}
		switch {
		case !slices.Contains(stateKinds(), row.state) || !slices.Contains(placeKinds(), row.place):
			t.Errorf("the table of moves (knownMoves) holds a move of %s toward true %s, which is no kind of state at a kind of place", row.state, row.place)
		case rows[move]:
			t.Errorf("the table of moves (knownMoves) holds the move of %s toward true %s twice", row.state, row.place)
		case row.none != "" && len(made[move]) > 0:
			t.Errorf("the table of moves (knownMoves) says that no input can move %s toward true %s (%s), and %q make that move", row.state, row.place, row.none, made[move])
		case row.none == "" && !slices.Contains(made[move], row.base+": "+row.known):
			t.Errorf("the table of moves (knownMoves) names the known positive at %s, %s, for the move of %s toward true %s, which it does not make; the known positives that make it: %q", row.base, row.known, row.state, row.place, made[move])
		}
		rows[move] = true
	}
	for _, state := range stateKinds() {
		for _, place := range placeKinds() {
			if !rows[[2]string{state, place}] {
				t.Errorf("the table of moves (knownMoves) has no row for the move of %s toward true %s", state, place)
			}
			if known := made[[2]string{state, place}]; len(known) > 0 {
				t.Logf("the move of %s toward true %s: %q", state, place, known)
			}
		}
	}
}

// sweepAgainst derives each input of base, the base name, in the reader's process p and compares its
// states with those of the base, as the process reads it: an input that moves a state toward true
// fails the test, unless the input has a right to it. The floor's digest holds the key of the base's
// line, the bytes that the process read of it (sweepCount.judged), so a base that a writer or code of
// the sweep changed, whose change the sweep would read both as its input's and as its want's, fails
// by the base's name. A right with allowed covers only the places where the input reads the state
// that allowed holds there. Two rights name no place, so an input that has one is judged by its right
// alone: a repository path of a tree written " " (pathOfOneSpace), whose tree then changed at a path
// that a tie covers, so the carry of each result across that commit ends, at each subject after it;
// and a parent left out of an approved block (parentLeftOut), whose statement then sits at the top of
// the tree, so the statement above it loses it, which can leave that one and each above it met. The
// places that such an input may move are those that the derivation itself reaches from its change,
// so a list of them would be a second derivation. An input whose files are the base's (sameFiles)
// fails the test too: its writer wrote nothing, and it would read as the base and pass. So does an
// input whose files are those of an earlier input of the list, byte for byte (the key of its line,
// readerLine): the sweep would read one case twice and the case that it means not at all, as when a
// writer or a table turned one input into another. The sweep also judges its known positives at
// the base (knownPositives), last, by the same code as each input, and fails by name unless it
// reported each, with exactly the lines that it is built to move (sweepCount.known); no floor counts
// them.
func sweepAgainst(t *testing.T, reader *readerProcess, name string, base fstest.MapFS, inputs []sweepInput) sweepCount {
	t.Helper()
	line, baseKey := readerLine(t, base)
	whole, err := reader.ask(t, line)
	if err != nil {
		t.Fatalf("the base: %v", err)
	}
	n := sweepCount{inputs: len(inputs), known: map[string][]string{}}
	n.judged = append(n.judged, baseKey[:]...)
	known := knownPositives(t, name, base, whole)
	inputs = append(slices.Clone(inputs), known...)
	seen := map[[sha256.Size]byte]string{}
	for _, in := range inputs {
		if sameFiles(t, base, in.fsys) {
			t.Errorf("%s: the input's files are the base's, so the sweep wrote nothing", in.what)
			continue
		}
		line, key := readerLine(t, in.fsys)
		if first, held := seen[key]; held {
			t.Errorf("%s: the input's files are those of the input %s, byte for byte, so the sweep reads one case twice", in.what, first)
			continue
		}
		seen[key] = in.what
		got, err := reader.ask(t, line)
		switch {
		case err != nil:
			if n.counted(in, key, 'x') {
				n.refused++
			}
		case maps.Equal(got, whole):
			if n.counted(in, key, 'r') {
				n.same++
			}
		default:
			toward := towardTrue(whole, got)
			if in.right != "" && in.allowed != nil {
				beyond := maps.Clone(got)
				maps.DeleteFunc(beyond, func(at, state string) bool { return in.allowed[at] == state })
				if rest := towardTrue(whole, beyond); len(rest) > 0 {
					n.report(t, in, rest, fmt.Sprintf("%s, beyond its right (%s): %s", in.what, in.right, strings.Join(rest, "; ")))
					continue
				}
			}
			switch {
			case len(toward) == 0:
				if n.counted(in, key, 'r') {
					n.moved++
				}
			case in.right != "":
				if n.counted(in, key, 'r') {
					n.moved++
					n.byRight++
					n.rights = append(n.rights, in.what)
				}
			default:
				n.report(t, in, toward, fmt.Sprintf("%s: %s", in.what, strings.Join(toward, "; ")))
			}
		}
	}
	for _, k := range known {
		switch moved, reported := n.known[k.what]; {
		case !reported:
			t.Errorf("the sweep judged its known positive at %s, %s, which moves %q toward true, and did not report it: its judgment no longer reports such an input", name, k.what, k.moves)
		case !slices.Equal(moved, k.moves):
			t.Errorf("the sweep reported its known positive at %s, %s, as moving %q toward true, where it moves %q: its judgment no longer reports each kind of state", name, k.what, moved, k.moves)
		}
	}
	return n
}

// sameFiles is whether the scenarios a and b hold the same files with the same contents: each JSON
// file as its decoded tree, since the base's files are as their author wrote them and a write is as
// encodeTree writes it, and each other file by its bytes.
func sameFiles(t *testing.T, a, b fstest.MapFS) bool {
	t.Helper()
	if !slices.Equal(slices.Sorted(maps.Keys(a)), slices.Sorted(maps.Keys(b))) {
		return false
	}
	for name, file := range a {
		switch {
		case bytes.Equal(file.Data, b[name].Data):
			continue
		case path.Ext(name) != ".json":
			return false
		}
		var x, y any
		if json.Unmarshal(file.Data, &x) != nil || json.Unmarshal(b[name].Data, &y) != nil || !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// A refusal of CI at a commit A makes its check read unknown at A, whatever else holds there, and
// the own results of A count nowhere, at A or where R8 would carry them; the results of each other
// commit stay (CheckStates, step 2; R48: "At that commit, such a check reads unknown, and no
// earlier result of it counts"). So adding one moves no check toward true, no statement or its own
// state toward met, and no wait toward ends, but by one right, which the input's data gives: where
// A holds a result of the check, taking that result out can let the results of other commits
// decide, and they can read true (refusedCommitsResults). The right covers each place of a subject
// at another commit than A where the input reads what the base reads with each result of the check
// whose commit is A (countedCommit: for a failing example of a program check of the code, the
// commit that its fault was planted on, and none for a record that counts from no commit) taken out
// of results.json, and no place at A, where R48 gives none. A query or an agent of production or of
// the graph has no result of a commit (CountsAtACommit), so a refusal of it at another commit than
// the subject's takes out nothing, whatever commit a result's test_commit names, and it has no
// right. The sweep adds one refusal at a time to uncited.json of each base (sweepBases), at each
// commit that a record of the base names, for each check of its approved spec that the base does
// not refuse there, and reads each one back as the reader reads it, against the refusals that it
// means, built before its writer runs (refusalAdded, refusalRead).
func TestAddingARefusalOfCIMovesAStateTowardTrueOnlyByTakingOutItsCommitsResults(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	floorsCover(t, "refusals", bases)
	reader := newSweepReader(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			p := reader.start(t)
			s, err := ReadScenario(base)
			if err != nil {
				t.Fatalf("the base: %v", err)
			}
			current, _ := s.Approved.Current()
			var inputs []sweepInput
			for _, commit := range namesOf(t, base)["commit"] {
				for _, c := range current.Checks {
					if _, refused := s.Uncited[commit][c.ID]; refused {
						continue
					}
					fsys := refusalAdded(t, base, commit, c.ID)
					in := sweepInput{what: fmt.Sprintf("uncited.json: a refusal of %s at %s", c.ID, commit), fsys: fsys}
					if CountsAtACommit(c) && slices.ContainsFunc(s.Results, func(r Result) bool { return r.Check == c.ID && countedCommit(s, c, r) == commit }) {
						in.right, in.allowed = refusedCommitsResults, withoutResultsOf(t, p, base, c, commit)
					}
					inputs = append(inputs, in)
				}
			}
			n := sweepAgainst(t, p, name, base, inputs)
			atFloor(t, "refusals", name, n)
			t.Logf("%d refusals added: %d refused, %d read as the base, %d read another state, %d of them toward true by right: %q",
				n.inputs, n.refused, n.same, n.moved, n.byRight, n.rights)
		})
	}
}

// refusedCommitsResults is the right of a refusal of CI at a commit that holds results of its
// check: those results count nowhere (R48), and the results of the other commits decide.
const refusedCommitsResults = `the results of the refused commit count nowhere, and the results of the other commits decide (R48: "no earlier result of it counts")`

// withoutResultsOf is the states of base, as the reader's process p reads them, with each result of
// the check c whose commit (countedCommit) is commit taken out of results.json, and with no state of a
// subject at commit: the places that a refusal of the check at commit may move toward true, each to
// the state given here. The results that it means, those of base but each of c whose commit is
// commit, as the reader reads them, and the place of each record to take out come from a decode of
// base of their own, made before the writer runs; the writer (resultsWithout) is handed a copy of
// those places and the tree of results.json decoded for it alone, and the reader reads its write
// back.
func withoutResultsOf(t *testing.T, p *readerProcess, base fstest.MapFS, c Check, commit string) map[string]string {
	t.Helper()
	s := decodedScenario(t, base)
	var out []int
	for i, r := range s.Results {
		if r.Check == c.ID && countedCommit(s, c, r) == commit {
			out = append(out, i)
		}
	}
	want := slices.DeleteFunc(slices.Clone(s.Results), func(r Result) bool { return r.Check == c.ID && countedCommit(s, c, r) == commit })
	fsys := editTree(t, base, "results.json", resultsWithout(slices.Clone(out)))
	if read, err := ReadScenario(fsys); err != nil || !reflect.DeepEqual(read.Results, want) {
		t.Fatalf("the sweep took the results of %s at %s out of results.json, and the reader reads %d results (%v), where it should read %d", c.ID, commit, len(read.Results), err, len(want))
	}
	states, err := p.states(t, fsys)
	if err != nil {
		t.Fatalf("the base with the results of %s at %s taken out: %v", c.ID, commit, err)
	}
	for i, subject := range s.Subjects {
		if subject.Commit == commit {
			maps.DeleteFunc(states, func(at, _ string) bool { return strings.HasPrefix(at, fmt.Sprintf("subject %d ", i+1)) })
		}
	}
	return states
}

// resultsWithout is the writer of withoutResultsOf: the tree of results.json that it is handed
// without each record at a place of out.
func resultsWithout(out []int) treeEdit {
	return func(v any) any {
		kept := []any{}
		for i, record := range v.([]any) {
			if !slices.Contains(out, i) {
				kept = append(kept, record)
			}
		}
		return kept
	}
}

// decodedScenario is the scenario of base as ReadScenario reads it, decoded for its caller alone.
func decodedScenario(t *testing.T, base fstest.MapFS) Scenario {
	t.Helper()
	s, err := ReadScenario(base)
	if err != nil {
		t.Fatalf("the base: %v", err)
	}
	return s
}

// plantedOnElsewhere is the right of a planted_on added to a failing example where it names a commit
// of the history: R8 decides whether that record counts at a subject, and where it does not, the
// older evidence decides, as with no such record.
const plantedOnElsewhere = `a planted_on that names a commit of the history counts only where R8 carries it; elsewhere older evidence decides`

// withoutResult is the states of base with result i of results.json taken out, as the reader's
// process p reads them.
func withoutResult(t *testing.T, p *readerProcess, base fstest.MapFS, i int) map[string]string {
	t.Helper()
	fsys := changedFile(t, base, "results.json", treeChange{place: []string{strconv.Itoa(i)}, out: true}, leftOutAt)
	states, err := p.states(t, fsys)
	if err != nil {
		t.Fatalf("the base with result %d taken out: %v", i+1, err)
	}
	return states
}

// countedCommit is the commit where the result r of the check c of the scenario s counts
// (CheckStates, steps 2 and 7), "" for none: for a failing example of a program check of the code,
// the commit that the step planted its fault on, where its record says where (unplanted) and the
// history holds that commit, and else none, since such a record counts from no commit, so that a
// refusal at the commit that it names gives no right to take it out; and for each other result, the
// commit where its job ran (resultCommit).
func countedCommit(s Scenario, c Check, r Result) string {
	if r.Kind != ResultFailingExample || c.Subject != SubjectCode || c.Procedure != ProcedureProgram {
		return resultCommit(r)
	}
	if _, held, err := s.History.Changed(r.PlantedOn, r.PlantedOn); unplanted(r) != "" || err != nil || !held {
		return ""
	}
	return r.PlantedOn
}

// addableMembers is each member that a record of a JSON file of a scenario can leave out (the
// twelve members marked zero that a file can leave out, memberMark), by the place of the list of
// its records and the kind of the values that the sweep gives it.
func addableMembers() map[string]addableRecords {
	return map[string]addableRecords{
		"results.json": {nil, [][2]string{{"commit", "commit"}, {"deploy", "deploy"}, {"graph_tx", "graph_tx"}, {"job", "job"},
			{"attempt", "attempt"}, {"fault_author", "author"}, {"test_authors", "authors"}, {"test_commit", "commit"},
			{"planted_on", "commit"}, {"fault_paths", "paths"}}},
		"subjects.json": {nil, [][2]string{{"graph_tx", "graph_tx"}}},
		"history.json":  {[]string{"commits"}, [][2]string{{"parent", "commit"}}},
	}
}

// addableRecords is the records of a JSON file to which the member sweep adds members: the place of
// their list, and each member with its kind (addableMembers).
type addableRecords struct {
	records []string
	members [][2]string // each member and its kind
}

// A member that a record leaves out, added, states one more fact of the record. No such input may
// move a state toward true: the sweep adds each member of addableMembers to each record of each base
// (sweepBases) that leaves it out, one at a time, with each value of its kind that a record of the
// base names and one that none names. A test_commit on a result of a check of the code moves
// nothing, since the commit of a result has one reading, its commit (resultCommit), for the judge
// and for the leg rule alike; one on a result of a query or an agent of production or of the graph
// moves nothing, also where CI refused the check at that commit, since such a check reads no commit
// (CountsAtACommit); and a commit on a result of a check of production or of the graph is refused.
// A planted_on alone, or fault_paths alone, on a failing example of a program check of the code
// that no step planted moves no state: it counts from planted_on only with both, and with one of
// the two it reads unknown with the reason that names the other (CheckStates, step 7). A planted_on
// that names a commit of the history has one right: where R8 does not carry that commit to a
// subject, the record counts nowhere there and the base without that record decides
// (plantedOnElsewhere). A planted_on that names no commit of the history has none: such a record
// reads unknown, planted_on_outside_history.
func TestAddingAMemberMovesNoStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	floorsCover(t, "members", bases)
	reader := newSweepReader(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			p := reader.start(t)
			s, err := ReadScenario(base)
			if err != nil {
				t.Fatalf("the base: %v", err)
			}
			names := namesOf(t, base)
			values := map[string][]any{"attempt": {2}}
			for kind, other := range map[string]string{"commit": "c9", "deploy": "d9", "graph_tx": "g9", "job": "other-job", "author": "other-author", "path": "packages/envoy/cmd/other/main.go"} {
				for slices.Contains(names[kind], other) {
					other += "x" // the value that no record of the base names: carry with a failing example planted on c9 names c9
				}
				for _, v := range append(slices.Clone(names[kind]), other) {
					values[kind] = append(values[kind], v)
					switch kind {
					case "author":
						values["authors"] = append(values["authors"], []any{v})
					case "path":
						values["paths"] = append(values["paths"], []any{v})
					}
				}
			}
			var inputs []sweepInput
			addable := addableMembers()
			for _, file := range slices.Sorted(maps.Keys(addable)) {
				var tree any
				if err := json.Unmarshal(base[file].Data, &tree); err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				list := tree
				for _, name := range addable[file].records {
					list = list.(map[string]any)[name]
				}
				for i, record := range list.([]any) {
					place := append(slices.Clone(addable[file].records), strconv.Itoa(i))
					for _, m := range addable[file].members {
						if _, set := record.(map[string]any)[m[0]]; set {
							continue
						}
						for _, v := range values[m[1]] {
							// The read back takes the member and its value from the input's words, and
							// changedFile builds the file that it means before the writer runs and hands the
							// writer copies of its own.
							fsys := changedFile(t, base, file, treeChange{place: append(slices.Clone(place), m[0]), value: v}, valueAt)
							in := sweepInput{what: fmt.Sprintf("%s %s with %s %s", file, treePointer(place), m[0], encodeTree(t, v)), fsys: fsys}
							if commit, named := v.(string); file == "results.json" && m[0] == "planted_on" && named {
								if _, held, err := s.History.Changed(commit, commit); err == nil && held {
									in.right, in.allowed = plantedOnElsewhere, withoutResult(t, p, base, i)
								}
							}
							inputs = append(inputs, in)
						}
					}
				}
			}
			n := sweepAgainst(t, p, name, base, inputs)
			atFloor(t, "members", name, n)
			t.Logf("%d members added: %d refused, %d read as the base, %d read another state", n.inputs, n.refused, n.same, n.moved)
		})
	}
}

// namesOf is each name that a record of the scenario fsys names, by its kind (commit, deploy,
// graph_tx, job, author, path), sorted, each once: the commits of the history, of the subjects, of
// the results, among them the commits that faults were planted on, of the job runs and of the maps
// of ties.json and uncited.json; the deploys and the transactions of the subjects and of the
// results; the jobs of the results and of the job runs; the authors of the results; and the paths
// of the trees of the history and of the faults of the results.
func namesOf(t *testing.T, fsys fstest.MapFS) map[string][]string {
	t.Helper()
	names := map[string]map[string]bool{}
	add := func(kind string, v any) {
		if s, ok := v.(string); ok && s != "" {
			if names[kind] == nil {
				names[kind] = map[string]bool{}
			}
			names[kind][s] = true
		}
	}
	read := func(file string) any {
		var tree any
		if err := json.Unmarshal(fsys[file].Data, &tree); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		return tree
	}
	for _, c := range read("history.json").(map[string]any)["commits"].([]any) {
		add("commit", c.(map[string]any)["id"])
		for path := range c.(map[string]any)["files"].(map[string]any) {
			add("path", path)
		}
	}
	for _, file := range []string{"subjects.json", "results.json", "jobs.json"} {
		for _, r := range read(file).([]any) {
			r := r.(map[string]any)
			add("commit", r["commit"])
			add("commit", r["test_commit"])
			add("commit", r["planted_on"])
			add("deploy", r["deploy"])
			add("graph_tx", r["graph_tx"])
			add("job", r["job"])
			add("author", r["fault_author"])
			for member, kind := range map[string]string{"test_authors": "author", "fault_paths": "path"} {
				if list, ok := r[member].([]any); ok {
					for _, v := range list {
						add(kind, v)
					}
				}
			}
		}
	}
	for _, file := range []string{"ties.json", "uncited.json"} {
		for commit := range read(file).(map[string]any) {
			add("commit", commit)
		}
	}
	sorted := map[string][]string{}
	for kind, set := range names {
		sorted[kind] = slices.Sorted(maps.Keys(set))
	}
	return sorted
}

// sweepInput is one input of the sweep: what it changed, for the line of a failure alone, the files
// of the scenario with it, the reason of its right to move a state toward true, "" for none, the
// states that the right allows, by place, nil for a right that covers each place, and whether it is
// one of the sweep's known positives (knownPositives), which the sweep must report and no floor
// counts, with moves, the lines of the states that it is built to move toward true.
type sweepInput struct {
	what    string
	fsys    fstest.MapFS
	right   string
	allowed map[string]string
	known   bool
	moves   []string
}

// pathOfOneSpace is the right of a repository path of a tree of the history written " ": a real
// path, since git holds a file of that name, so the tree changed at a path that a tie covers.
const pathOfOneSpace = `the path " " of the tree is a real path, which git holds, so the tree changed at a path that a tie covers, and the carry ends (R8)`

// sweepInputs is each input of the sweep of the scenario base, and base with each JSON file written
// again by the sweep's own decoder and encoder, unchanged, which the sweep reads as base. Each input
// is read back against the change that its words name, which changedFile builds into the file that
// it means before the writer runs; the writers are functions of the package (nullAt, leftOutAt,
// renamedAt, valueAt, takenOut), each handed copies of its own.
func sweepInputs(t *testing.T, base fstest.MapFS) ([]sweepInput, fstest.MapFS) {
	t.Helper()
	var inputs []sweepInput
	entryMaps, pathPlaces := sweepMaps(), sweepPaths()
	roundTrip := maps.Clone(base)
	with := func(what, name string, want treeChange, write treeWriter) *sweepInput {
		inputs = append(inputs, sweepInput{what: what, fsys: changedFile(t, base, name, want, write)})
		return &inputs[len(inputs)-1]
	}
	for _, name := range slices.Sorted(maps.Keys(base)) {
		out := filesWritten(t, base, map[string][]byte{name: nil}, takenOut(name))
		inputs = append(inputs, sweepInput{what: name + " taken out", fsys: out})
		if path.Ext(name) != ".json" {
			continue
		}
		var tree any
		if err := json.Unmarshal(base[name].Data, &tree); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		roundTrip[name] = &fstest.MapFile{Data: encodeTree(t, tree)}
		with(name+" as null", name, treeChange{}, nullAt)
		var walk func(v any, at []string)
		walk = func(v any, at []string) {
			switch v := v.(type) {
			case map[string]any:
				entries := slices.ContainsFunc(entryMaps[name], func(p []string) bool { return placeMatches(p, at) })
				paths := slices.ContainsFunc(pathPlaces[name].keysOf, func(p []string) bool { return placeMatches(p, at) })
				for _, key := range slices.Sorted(maps.Keys(v)) {
					place := append(slices.Clone(at), key)
					with(name+" "+treePointer(place)+" as null", name, treeChange{place: place}, nullAt)
					if !entries {
						with(name+" "+treePointer(place)+" left out", name, treeChange{place: place, out: true}, leftOutAt)
					}
					var renames []string
					if entries {
						renames = namesOfNothing()
					}
					if paths {
						renames = append(slices.Clone(renames), notRepoPaths(key)...)
					}
					for _, renamed := range renames {
						if _, taken := v[renamed]; !taken {
							in := with(fmt.Sprintf("%s %s as the key %q", name, treePointer(place), renamed), name, treeChange{place: place, value: renamed, renamed: true}, renamedAt)
							if paths && renamed == " " {
								in.right = pathOfOneSpace
							}
						}
					}
					walk(v[key], place)
				}
			case []any:
				for i, x := range v {
					place := append(slices.Clone(at), strconv.Itoa(i))
					with(name+" "+treePointer(place)+" as null", name, treeChange{place: place}, nullAt)
					walk(x, place)
				}
			case string:
				forms := namesOfNothing()
				if slices.ContainsFunc(pathPlaces[name].values, func(p []string) bool { return placeMatches(p, at) }) {
					forms = append(forms, notRepoPaths(v)...)
				}
				for _, form := range forms {
					if form != v {
						with(fmt.Sprintf("%s %s as %q", name, treePointer(at), form), name, treeChange{place: at, value: form}, valueAt)
					}
				}
			}
		}
		walk(tree, nil)
	}
	return inputs, roundTrip
}

// towardTrue is each state of got that reads true, met or ends where whole reads otherwise.
func towardTrue(whole, got map[string]string) []string {
	var toward []string
	for _, at := range slices.Sorted(maps.Keys(got)) {
		if v := got[at]; (v == checkTrue || v == statementMet || v == "ends") && whole[at] != v {
			toward = append(toward, fmt.Sprintf("%s: %q, and %q in the whole scenario", at, v, whole[at]))
		}
	}
	return toward
}

// recordKinds is each kind of record of a scenario, a fact that CI or the repository recorded about
// a check, by its file and its place: a result, a run of a job, a refusal of CI, a tie, a commit of
// the history and a file of a commit's tree. The approved spec, the subjects and the units are what
// the derivation judges and where, not records.
func recordKinds() map[string][][]string {
	return map[string][][]string{
		"results.json": {{"*"}},
		"jobs.json":    {{"*"}},
		"uncited.json": {{"*"}, {"*", "*"}},
		"ties.json":    {{"*"}, {"*", "*"}},
		"history.json": {{"commits", "*"}, {"commits", "*", "files", "*"}},
	}
}

// A record taken out whole is another input, not one that says nothing: the history in which the
// fact did not happen. A record that states a fact against a check, taken out, can move a state
// toward true by right, and each kind of record has a row here, with the state of the check with
// the record and with it taken out, and its reason; a kind with no row fails the test. A file taken
// out of a commit's tree is not there at that commit, so it changed, and the carry of each result
// across it ends, a failure's as a pass's (R8: "A result at one commit also counts at another
// commit when no code that the check runs changed between the two"), as a changed label ends it:
// it moves a state away from true where a pass carried, and toward true where a failure carried (a
// row of each), as a path of a tree written " " does in
// TestNoInputThatSaysNothingMovesAStateTowardTrue. Each row derives the check at the subject of its
// scenario with the record, and with the record taken out; setup makes the scenario first.
func TestTakingOutAWholeRecordMovesAStateOnlyByItsRight(t *testing.T) {
	t.Parallel()
	const c = "a-restart-loses-no-message"
	const q = "production-delivery-takes-under-a-second"
	type row struct {
		kind, reason  string
		scenario      string
		setup         func(t *testing.T, fsys fstest.MapFS) fstest.MapFS
		file          string
		place         []string
		subject       int    // from 1
		check         string // whose state the row reads
		with, without string // the state of the check with the record and with it taken out
	}
	// carriedFailure is carry with a failure of c at c1 after its pass, which carries to c2, and a
	// pass at c2 after it and a caught failing example planted on c2: c2 reads unknown (disagree).
	carriedFailure := func(t *testing.T, fsys fstest.MapFS) fstest.MapFS {
		return editTree(t, fsys, "results.json", failureCarriedToC2())
	}
	rows := []row{
		{kind: "a file of a commit's tree", reason: "a tied file that the later commit does not hold changed, so c1's pass carries no more to c2 (R8)",
			scenario: "carry", file: "history.json", place: []string{"commits", "2", "files", "packages/envoy/cmd/listener/main.go"},
			subject: 1, check: c, with: "true", without: "unknown"},
		{kind: "a file of a commit's tree", reason: "a tied file that the later commit does not hold changed, so c1's failure carries no more to c2, and c2's own pass and caught failing example count (R8)",
			scenario: "carry", setup: carriedFailure, file: "history.json", place: []string{"commits", "2", "files", "packages/envoy/cmd/listener/main.go"},
			subject: 1, check: c, with: "unknown", without: "true"},
		{kind: "a failing result", reason: "the failure is the record against the check, and the history without it holds no failure (R7: \"a later failure cancels an earlier pass\")",
			scenario: "silent-leg", file: "results.json", place: []string{"5"},
			subject: 3, check: q, with: "false", without: "true"},
		{kind: "a run of a job", reason: "with no run of leg arm that ended, no leg is silent (R9: \"after a run of its job ends\")",
			scenario: "silent-leg", file: "jobs.json", place: []string{"1"},
			subject: 1, check: c, with: "unknown", without: "true"},
		{kind: "the refusals of CI at a commit", reason: "CI then refused no check at the commit (R48)",
			scenario: "refusal", file: "uncited.json", place: []string{"c2"},
			subject: 1, check: c, with: "unknown", without: "true"},
		{kind: "a refusal of CI", reason: "CI then refused no check at the commit (R48)",
			scenario: "refusal", file: "uncited.json", place: []string{"c2", c},
			subject: 1, check: c, with: "unknown", without: "true"},
		{kind: "the ties at a commit", reason: "no tie is then known at the commit, and a tie that is not known carries no result (R18, R8), so the failure there counts nowhere else",
			scenario: "carry", setup: carriedFailure, file: "ties.json", place: []string{"c1"},
			subject: 1, check: c, with: "unknown", without: "true"},
		{kind: "a tie", reason: "the tie is then not known at the commit, and a tie that is not known carries no result (R18, R8), so the failure there counts nowhere else",
			scenario: "carry", setup: carriedFailure, file: "ties.json", place: []string{"c1", c},
			subject: 1, check: c, with: "unknown", without: "true"},
		{kind: "a commit of the history", reason: "the history then holds no tree of the commit, so no result carries from it (R8), and the failure there counts nowhere else",
			scenario: "carry", setup: carriedFailure, file: "history.json", place: []string{"commits", "1"},
			subject: 1, check: c, with: "unknown", without: "true"},
	}
	kinds := recordKinds()
	for kind := range maps.Keys(kinds) {
		for _, pattern := range kinds[kind] {
			if !slices.ContainsFunc(rows, func(r row) bool { return r.file == kind && placeMatches(pattern, r.place) }) {
				t.Errorf("no row takes out a record of %s at %s", kind, treePointer(pattern))
			}
		}
	}
	reader := newSweepReader(t)
	p := reader.start(t)
	for _, r := range rows {
		base := scenarioFS(t, filepath.Join("testdata", "scenarios", r.scenario))
		if r.setup != nil {
			base = r.setup(t, base)
		}
		without := changedFile(t, base, r.file, treeChange{place: r.place, out: true}, leftOutAt)
		for _, read := range []struct {
			fsys fstest.MapFS
			want string
			form string
		}{{base, r.with, "with"}, {without, r.without, "without"}} {
			states, err := p.states(t, read.fsys)
			if err != nil {
				t.Errorf("%s, %s the record: %v", r.kind, read.form, err)
				continue
			}
			at := ""
			for key := range states {
				if strings.HasPrefix(key, fmt.Sprintf("subject %d ", r.subject)) && strings.HasSuffix(key, " check "+r.check) {
					at = key
				}
			}
			if got := states[at]; got != read.want {
				t.Errorf("%s, %s the record %s %s: the check reads %q, want %q (%s)", r.kind, read.form, r.file, treePointer(r.place), got, read.want, r.reason)
			}
		}
	}
}

// failureCarriedToC2 is the writer of carry's results.json that adds a failure of its first
// record's check at c1 after its pass, a pass at c2 after it, and a copy of its caught failing
// example planted on c2 (TestTakingOutAWholeRecordMovesAStateOnlyByItsRight).
func failureCarriedToC2() treeEdit {
	return func(v any) any {
		results := v.([]any)
		run := func(commit, outcome, at string, seq int) map[string]any {
			r := maps.Clone(results[0].(map[string]any))
			r["commit"], r["outcome"], r["at"], r["seq"] = commit, outcome, at, seq
			return r
		}
		fault := maps.Clone(results[1].(map[string]any))
		fault["commit"], fault["planted_on"], fault["at"], fault["seq"] = "f-c2", "c2", "2026-11-13T09:05:00Z", 7
		return append(results, run("c1", "fail", "2026-11-12T10:10:00Z", 5), run("c2", "pass", "2026-11-13T09:00:00Z", 6), fault)
	}
}

// refusalAdded is a copy of base, of its own (editTree), with CI's refusal of the check id at
// commit added to uncited.json by its writer (refusalAt), read back by the reader against the
// refusals that the sweep means, which it builds before the writer runs from a decode of base of
// their own: those of base, and that one with the rule test-did-not-run (refusalRead).
func refusalAdded(t *testing.T, base fstest.MapFS, commit, id string) fstest.MapFS {
	t.Helper()
	want := map[string]map[string]string{commit: {}}
	for at, checks := range decodedScenario(t, base).Uncited {
		want[at] = maps.Clone(checks)
	}
	want[commit][id] = "test-did-not-run"
	fsys := editTree(t, base, "uncited.json", refusalAt(commit, id))
	refusalRead(t, fsys, commit, id, want)
	return fsys
}

// refusalAt is the writer of the refusal sweep: the tree of uncited.json that it is handed with CI's
// refusal of the check id at commit, by the rule test-did-not-run.
func refusalAt(commit, id string) treeEdit {
	return func(v any) any {
		uncited := v.(map[string]any)
		checks := map[string]any{}
		if at, held := uncited[commit].(map[string]any); held {
			checks = maps.Clone(at)
		}
		checks[id] = "test-did-not-run"
		uncited[commit] = checks
		return uncited
	}
}

// refusalRead fails the test unless the scenario fsys, to which the refusal sweep added CI's
// refusal of the check id at commit, reads, through the reader, as want, the refusals that the
// sweep means, and no other refusal: the read back of the refusal by its commit, its check and its
// rule, so that a writer that writes the refusal of another check, at another commit, or an entry
// of the commit that holds no refusal, fails by name.
func refusalRead(t *testing.T, fsys fstest.MapFS, commit, id string, want map[string]map[string]string) {
	t.Helper()
	if read, err := ReadScenario(fsys); err != nil || !reflect.DeepEqual(read.Uncited, want) {
		t.Fatalf("the sweep wrote CI's refusal of %s at %s, and the reader reads the refusals %v (%v), where it should read %v", id, commit, read.Uncited, err, want)
	}
}
