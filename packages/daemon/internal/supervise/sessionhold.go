package supervise

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
// (a planner); a resumed launch registers the session and file it recorded,
// which holds nothing new; and the launched controllers re-registered at most 5 sessions. A session
// gives at most 4 names (RecordedHolds), so that claim holds at most 42 × 4 + 8 (the two sessions
// the migration held for it) = 176 names, and 10× is 1,760. Even the most-launched claim, at
// generation 193 (an architect), would hold 193 × 4 + 8 = 780 if every launch
// had started a fresh session. The average is about 4.5.
const MaxHeldNamesPerClaim = 2048

// ErrHeldNamesCap is the store's answer to a hold that would take its claim past
// MaxHeldNamesPerClaim (Store.HoldSessions): it records none of the call's names, and the claim
// keeps every name it holds.
var ErrHeldNamesCap = errors.New("the claim would hold more session names than one claim may")

// heldNamesCapLine is the Warn line of a registration refused at MaxHeldNamesPerClaim. Its
// `release` field is the claim's ReleaseOldNamesStatement, the operator's way to let it start a new
// session, and its `release_after` field the earliest time that statement can free anything
// (ReleaseAfter); an operator reads the line whole, so it is pinned here.
const heldNamesCapLine = "supervise: refused a session whose names would take the claim past the most one claim may hold; a session it records still resumes, and the statement frees the names of its older sessions for a new one, once release_after has passed"

// ReleaseWindow is how long after a claim's last session switch, and after a hold was written,
// ReleaseOldNamesStatement waits before it frees a hold: the broker's default carry window,
// 12 hours, plus its longest lease, an hour, plus 5 minutes for the two sweeps between a lease's
// lapse and its carry's end, each at most BROKER_SWEEP_SECONDS' maximum, 60 seconds
// (ReleaseOldNamesStatement says why).
const ReleaseWindow = 13*time.Hour + 5*time.Minute

// ReleaseAfter is the earliest time ReleaseOldNamesStatement can free any of the claim's holds: its
// last session switch plus ReleaseWindow, rounded up to the second so a time written to the second
// is never before the statement's own gate, or the zero time for a claim that has never switched.
// The statement frees a never-switched claim's holds by their own age, and only those whose names
// contain no id of its session, so no single time is that claim's (heldNamesCapLine's field says
// so). A claim refused at the cap that needs a new session was usually just given one by an
// operator's forget-session, so the statement frees nothing for a whole ReleaseWindow after it
// (review round 5 of #4220); the claim's record does not show the switch on any operator's view, so
// the refusal's line gives the time.
func ReleaseAfter(c Claim) time.Time {
	if c.Abandoned.At.IsZero() {
		return time.Time{}
	}
	after := c.Abandoned.At.Add(ReleaseWindow).UTC()
	if whole := after.Truncate(time.Second); !whole.Equal(after) {
		after = whole.Add(time.Second)
	}
	return after
}

// ReleaseOldNamesStatement is the statement an operator runs to let a claim at MaxHeldNamesPerClaim
// start a new session again: it frees the claim's holds recorded more than ReleaseWindow, 13 hours
// 5 minutes, ago, and only once the claim's last session switch is more than that ago, except
// every name of the session it records and of the one it last abandoned. A name of either session
// is kept when it is that session's id, contains that id, or is a part of that session's recorded
// file path, so a hold of any of those stays. That keeps the names of the recorded file, and the
// names of every earlier file of the session whose name contains its id, as an Oh My Pi
// `<timestamp>_<id>.jsonl` does; an earlier file of the current session whose name contains no id
// is freed, which frees no grant, since the broker enrolls a session by its id (review round 5 of
// #4220).
//
// The window counts from the switch, not from the hold (review R4-1 of #4220): a hold is written
// when its session is first registered, and the session may have run long after. A claim changes
// sessions only by forgetting the one it records (forgetSession), which writes the switch's time
// into the abandoned session's `at`, and every session before the one it abandoned had ended by
// then. The broker keeps an ended session's grants for its carry window, which counts from when its
// enrollment went dormant: that is the daemon's revoke as it lets the process go, or, if that
// revoke fails, the enrollment's lapse at its lease, at most BROKER_LEASE_SECONDS' maximum, an hour,
// after the process ended. The broker marks the enrollment dormant at the sweep after that, and
// ends its carry at a sweep after the carry window, each sweep at most BROKER_SWEEP_SECONDS'
// maximum, 60 seconds, after its moment. So 13 hours 5 minutes is the default carry window
// (BROKER_SESSION_CARRY_SECONDS, 43200) plus the longest lease (review F1 of #4220's round 4) plus
// two sweeps with room (review round 7): a switch older than that means every session the statement
// frees went dormant, and lost its carry, longer ago than the window, and the broker has swept
// their grants. A claim that later takes a freed name takes no grant of it, and a transcript
// within the project is readable to its roles anyway. A claim that has never switched, whose
// abandoned session is empty, frees by its holds' age alone, and never frees a name that contains
// its session's id: such a claim at the cap needs one forget-session, a second switch, and the
// window after it.
//
// Two cases the window does not cover (review rounds 5, 6 and 7 of #4220), both while no broker is
// serving, since a lease does not run then. During an outage the old leases stay live and the
// carry has not begun, so the statement must not run at all while no broker serves. When a broker
// serves again, it caps every running lease at its return plus one lease, BROKER_LEASE_SECONDS
// (envoy's enroll.ServingClock, which logs `broker: gave back the time no broker was serving to
// every lease still running when the last broker served`), so an enrollment whose revoke the
// daemon missed goes dormant up to one lease, and a sweep, after the broker's return; if the broker
// came back later than an hour less one lease after the claim's last switch, the switch plus the
// window is too soon. The daemon cannot see either, and its own missed revoke may log nothing,
// since it can exit between tries and its boot does not retry them. So an operator runs the
// statement only while a broker serves, and one who finds that broker line after the claim's last
// switch waits the window after the line, not after the switch.
//
// The daemon is not told the broker's settings: they are the broker's own (envoy's broker/config),
// read by its sweeper and answered on no route the daemon calls, so the statement writes the
// defaults, and a deployment with a longer carry window puts that window plus an hour and five
// minutes in both intervals. The project and token are written as SQL literals, their quotes
// doubled.
func ReleaseOldNamesStatement(project string, token claim.Token) string {
	window := fmt.Sprintf("interval '%d seconds'", int64(ReleaseWindow/time.Second))
	return "delete from claim_sessions h using claims c" +
		" where c.project = h.project and c.token = h.claim" +
		" and h.project = " + SQLLiteral(project) + " and h.claim = " + SQLLiteral(string(token)) +
		" and h.recorded_at < now() - " + window +
		" and coalesce((c.abandoned_session ->> 'at')::timestamptz, '-infinity') < now() - " + window +
		" and h.name <> c.session and h.name <> coalesce(c.abandoned_session ->> 'session', '')" +
		" and (c.session = '' or strpos(h.name, c.session) = 0)" +
		" and (coalesce(c.abandoned_session ->> 'session', '') = '' or strpos(h.name, c.abandoned_session ->> 'session') = 0)" +
		" and strpos(c.session_file || ' ' || coalesce(c.abandoned_session ->> 'sessionFile', ''), h.name) = 0"
}

// SQLLiteral is s as a PostgreSQL string literal under standard_conforming_strings, the default
// since PostgreSQL 9.1: quoted, with each quote doubled, so no text of s ends the literal. The
// statements the daemon prints for an operator to run write every name an agent chose this way.
func SQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

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
// (plan E's t-e-54). The daemon enrolls the session a claim's agent reports
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
// A name of the current session the store answered as this claim's is kept and not asked again
// while the claim records that session: the enrollment that follows a registration asks nothing
// more, and forgetSession empties the cache (Machine.heldNames). An operator's release may free a
// cached name the current session's recorded file no longer gives, an earlier file of it whose name
// contains no id; the cache keeps that name until the forget, which moves no grant, since the broker
// enrolls a session by its id, and the claim's own resume and enrollment use the recorded file and
// id, whose names the release keeps. A refusal
// is asked again every time, because an operator settles a name held for no claim by recording its
// rightful holder, and that claim's next decision on this same machine must see it. A hold that
// would take the claim past MaxHeldNamesPerClaim is refused with claim.HeldNamesCapRefusal and
// records nothing; one of names the claim holds already adds none, so a claim that records a
// session still resumes it, while one that needs a new session starts none until an operator runs
// the line's ReleaseOldNamesStatement after its `release_after` (ReleaseAfter).
// A store that does not answer refuses nothing: its error
// wraps errHoldUnanswered, which a launch tries again at the probe interval charging nothing
// (launchNow), and any other decision is asked again by the next one.
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
		releaseAfter := "none: the claim has never switched sessions; the statement frees only holds more than 13 hours 5 minutes old whose names contain no id of its session"
		if after := ReleaseAfter(m.claim); !after.IsZero() {
			releaseAfter = after.Format(time.RFC3339)
		}
		m.log.Warn(heldNamesCapLine,
			"claim", m.claim.Token, "names", len(ask), "cap", MaxHeldNamesPerClaim, "error", err,
			"release", ReleaseOldNamesStatement(m.claim.Project, m.claim.Token), "release_after", releaseAfter)
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
