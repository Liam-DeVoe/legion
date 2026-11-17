package leakguard

import "strings"

// RefusedBy is the check a refusal names. Its three values are the three constants below: a named
// type rather than a bare string, so a reader of a Refusal sees which values the field holds. An
// untyped string constant still converts to it, so the type documents the three values and does
// not enforce them.
type RefusedBy string

// The three sentences a refusal names the refusing check by. byRules is this package's own rule
// pass; bySharedCheck is the shared public leak check, whose own report the refusal carries; and
// byKnives is a push's knives pass (Guard.knives), the forbidden lists of the knives registry
// entries that name the push's destination.
const (
	byRules       RefusedBy = "this repository's own private-name rules match it"
	bySharedCheck RefusedBy = "the shared public leak check refused it"
	byKnives      RefusedBy = "the knives forbidden list of the destination's registry entry matches it"
)

// Refusal is a write refused because of the text it carries. It is one half of this package's
// failure vocabulary; the other is *SetupFailure. Both refuse the write, and a call site tells
// them apart with errors.As to say which one it met.
type Refusal struct {
	// What is the write, in the entry point's words: "the push to origin", "the gh call".
	What string
	// By names the check that refused: byRules, bySharedCheck or byKnives.
	By RefusedBy
	// Lines is the report. The rule pass writes one line per refusal, each naming the record and
	// the class and nothing else, with a path masked as check-private-names.sh masks one. The
	// knives pass writes one line per record and term, naming the file, the line and the term's
	// number in its entry's list, never the term (knivesLines). A refusal by the shared check
	// carries that check's own report instead, which is the only account of it anyone gets.
	Lines []string
	// Advice is what the refused writer can do about the refusal, printed on a line of its own
	// after the report, and "" where the check has nothing to add. Only the knives pass sets it
	// (knivesAdvice), and it names no repository and no term.
	Advice string
}

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("LEAK CHECK: refusing ")
	b.WriteString(r.What)
	b.WriteString(": ")
	b.WriteString(string(r.By))
	for _, line := range r.Lines {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	if r.Advice != "" {
		b.WriteString("\n")
		b.WriteString(r.Advice)
	}
	return b.String()
}

// SetupFailure is a write refused because a check could not be made to run: no shared check
// resolved, the shared check exited 2 or some status it does not document, the rule engine failed,
// a destination's visibility could not be read, or a push's knives registries, remote URLs, or the
// refs of its destination or an upstream could not be read. It refuses the write exactly as a
// *Refusal does — a check that did not run has cleared nothing — and says so in the shared
// check's own words, so that a person reading a log can tell a broken pod from a write that names
// something private.
type SetupFailure struct {
	// What is the write, in the entry point's words, as a Refusal's is.
	What string
	// Why is what could not be made to run.
	Why string
	// Err is the failure underneath, where there is one.
	Err error
}

func (f *SetupFailure) Error() string {
	message := "LEAK CHECK SETUP ERROR: " + f.What + ": " + f.Why
	if f.Err != nil {
		message += ": " + f.Err.Error()
	}
	return message
}

func (f *SetupFailure) Unwrap() error {
	return f.Err
}
