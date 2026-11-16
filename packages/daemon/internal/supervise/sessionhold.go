package supervise

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/claudelaunch"
)

// errHoldUnanswered is a hold the store did not answer (holdRecorded): it says nothing about the
// claim or its session, so it refuses nothing and is charged to no budget.
var errHoldUnanswered = errors.New("the store did not answer the session's hold")

// MaxHeldNamesPerClaim is the most names of sessions one claim may hold (holdRecorded). Every
// registration of a new session, and every re-registration naming a new file, holds its names,
// and nothing deletes a hold, so without a bound one role could grow claim_sessions without end
// and make every boot that reads it slower (review N2 of #4220).
//
// The number: 10× what the busiest claim in production could hold, rounded up to a power of two.
// Over 28 days of the daemons' `supervise: launched` lines (2026-10-20 to 2026-11-16: 11,391
// claims, 36,345 launches), the most fresh sessions one claim launched was 42
// (legion-infra-infra-3330-planner); a resumed launch registers the session and file it recorded,
// which holds nothing new; and the launched controllers re-registered at most 5 sessions. A session
// gives at most 4 names (RecordedHolds), so that claim holds at most 42 × 4 + 8 (the two sessions
// the migration held for it) = 176 names, and 10× is 1,760. Even the most-launched claim, at
// generation 193 (legion-infra-infra-1148-architect), would hold 193 × 4 + 8 = 780 if every launch
// had started a fresh session. The average is about 4.5.
const MaxHeldNamesPerClaim = 2048

// ErrHeldNamesCap is the store's answer to a hold that would take its claim past
// MaxHeldNamesPerClaim (Store.HoldSessions): it records none of the call's names, and the claim
// keeps every name it holds.
var ErrHeldNamesCap = errors.New("the claim would hold more session names than one claim may")

// HoldKind is which of a session's names a hold keeps (Hold): each is a key some resume path
// finds a session by.
type HoldKind string

const (
	// HoldSessionID is a session id: the one an agent registers, which the secrets broker enrolls
	// as its wake address (ensureEnrolled), and the one a Claude Code launch resumes, `--resume
	// <id>`, read from its transcript's name (claudelaunch.ResumeSession).
	HoldSessionID HoldKind = "session"
	// HoldFileName is a session file's base name (filepath.Base). It is every key a resume finds
	// a session by its file with: an Oh My Pi launch resumes the recorded path (`--resume=<file>`,
	// runtime/sandbox's agentArgv and the tmux runtime's launcher), the launcher finds the
	// session's row in the session table by that path (launcher's resumable and claudeKeeper), and
	// where the path is missing the tmux runtime resumes a file of the same name one folder below a
	// sessions directory (sessionFileOf). Two paths that are one have one name, so holding the name
	// holds the path too: no hold of the path alone is kept, since it could refuse nothing the
	// name's hold does not.
	HoldFileName HoldKind = "file-name"
	// HoldFileID is the session id an Oh My Pi file name `<timestamp>_<id>.jsonl` gives, the part
	// after its first `_`: the tmux runtime also resumes any `*_<id>.jsonl` there (newestSession).
	HoldFileID HoldKind = "file-id"
)

// Hold is one name of a session, held to the first claim of its project that recorded it
// (holdRecorded).
type Hold struct {
	Kind HoldKind
	Name string
}

// RecordedHolds is every name a claim's record of session and sessionFile gives its session, each
// a key some resume path or the secrets broker finds a session by (HoldKind), so that no claim
// registers, relaunches into or enrolls another claim's session by any of them: the session id;
// the file's base name, which holds its path too (HoldFileName); the id its Oh My Pi name
// `<timestamp>_<id>.jsonl` gives; and the Claude Code session a file named `<uuid>.jsonl` gives,
// where that is not the session id itself. The last is wider than a Claude Code resume, which also
// asks for a `projects` directory two levels up (claudelaunch.Transcript): it refuses more, never
// less, and no Oh My Pi file is named for a bare uuid. An empty session or file names nothing, and
// a session id or file name longer than claim.MaxSessionNameBytes names nothing either: a
// registration of one is refused (checkSessionNames), and one a daemon from before the bound
// recorded is left unheld rather than stopping the boot that would hold it. The claim_sessions
// migration's backfill spells the same rule in SQL, and its test holds the two to one answer.
func RecordedHolds(session, sessionFile string) []Hold {
	var holds []Hold
	if session != "" && len(session) <= claim.MaxSessionNameBytes {
		holds = append(holds, Hold{Kind: HoldSessionID, Name: session})
	}
	if sessionFile == "" {
		return holds
	}
	base := filepath.Base(sessionFile)
	if len(base) > claim.MaxSessionNameBytes {
		return holds
	}
	holds = append(holds, Hold{Kind: HoldFileName, Name: base})
	if stem, ok := strings.CutSuffix(base, ".jsonl"); ok {
		if _, id, ok := strings.Cut(stem, "_"); ok && id != "" {
			holds = append(holds, Hold{Kind: HoldFileID, Name: id})
		}
		if claudelaunch.IsSessionID(stem) && stem != session {
			holds = append(holds, Hold{Kind: HoldSessionID, Name: stem})
		}
	}
	return holds
}

// checkSessionNames refuses a registration whose session id, or whose session file's base name, is
// longer than claim.MaxSessionNameBytes (claim.SessionNameTooLong): such a name could not be held,
// and no real session has one.
func checkSessionNames(session, sessionFile string) error {
	if len(session) > claim.MaxSessionNameBytes {
		return claim.SessionNameTooLong("session id", len(session))
	}
	if base := filepath.Base(sessionFile); sessionFile != "" && len(base) > claim.MaxSessionNameBytes {
		return claim.SessionNameTooLong("session file's name", len(base))
	}
	return nil
}

// holdRecorded holds every name a claim's record gives its session (RecordedHolds) to the claim,
// whatever the claim holding one became since: queued, working, idle, suspended, failed or retired
// (dispatch://INFRA-840, plan E's t-e-54). The daemon enrolls the session a claim's agent reports
// with the secrets broker, whose enrollment takes over the grants and pending requests of every
// other enrollment of the same pod account and session, and a relaunch resumes the session the
// record names, its transcript read and written; every pod of a project enrolls under the
// project's one account. So the names are held before anything records, resumes or enrolls them:
// a claim may take only a name it recorded before or one no claim has recorded, and the store
// keeps which claim recorded each first (Store.HoldSessions), all of one record's names or none.
// Any other is refused with claim.SessionHeldRefusal, which names no other claim; the log names
// the holder. A name held for no claim (two claims recorded it before the hold) is refused to
// every claim.
//
// A name the store answered as this claim's is kept and not asked again, since a claim's hold
// never changes once made: the enrollment that follows a registration asks nothing more. A refusal
// is asked again every time, because an operator settles a name held for no claim by recording its
// rightful holder, and that claim's next decision on this same machine must see it. A hold that
// would take the claim past MaxHeldNamesPerClaim is refused with claim.HeldNamesCapRefusal and
// records nothing; one of names the claim holds already adds none, so the claim still resumes its
// own session. A store that does not answer refuses nothing: its error wraps errHoldUnanswered,
// which a launch tries again at the probe interval charging nothing (launchNow), and any other
// decision is asked again by the next one.
func (m *Machine) holdRecorded(ctx context.Context, session, sessionFile string) error {
	var ask []Hold
	for _, h := range RecordedHolds(session, sessionFile) {
		if !m.heldNames[h] {
			ask = append(ask, h)
		}
	}
	if len(ask) == 0 {
		return nil
	}
	var holders []claim.Token
	if err := m.write(ctx, func(ctx context.Context) error {
		var err error
		holders, err = m.deps.Store.HoldSessions(ctx, m.claim.Project, m.claim.Token, ask)
		return err
	}); errors.Is(err, ErrHeldNamesCap) {
		m.log.Warn("supervise: refused a session whose names would take the claim past the most one claim may hold",
			"claim", m.claim.Token, "names", len(ask), "cap", MaxHeldNamesPerClaim, "error", err)
		return fmt.Errorf("hold the session of %s: %w", m.claim.Token, claim.HeldNamesCapRefusal(MaxHeldNamesPerClaim))
	} else if err != nil {
		return fmt.Errorf("hold the session of %s: %w: %w", m.claim.Token, errHoldUnanswered, err)
	}
	if len(holders) != len(ask) {
		return fmt.Errorf("hold the session of %s: %w: it answered %d holders for %d names", m.claim.Token, errHoldUnanswered, len(holders), len(ask))
	}
	for i, h := range ask {
		if holders[i] != m.claim.Token {
			m.log.Warn("supervise: refused a session another claim recorded first",
				"kind", h.Kind, "name", h.Name, "claim", m.claim.Token, "holder", holders[i])
			return fmt.Errorf("%s as %s %s: %w", m.claim.Token, h.Kind, h.Name, claim.SessionHeldRefusal(h.Name))
		}
	}
	if m.heldNames == nil {
		m.heldNames = map[Hold]bool{}
	}
	for _, h := range ask {
		m.heldNames[h] = true
	}
	return nil
}
