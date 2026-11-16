package supervise

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/claudelaunch"
)

// HoldKind is which of a session's names a hold keeps (Hold): each is a key some resume path
// finds a session by.
type HoldKind string

const (
	// HoldSessionID is a session id: the one an agent registers, which the secrets broker enrolls
	// as its wake address (ensureEnrolled), and the one a Claude Code launch resumes, `--resume
	// <id>`, read from its transcript's name (claudelaunch.ResumeSession).
	HoldSessionID HoldKind = "session"
	// HoldSessionFile is a session file's path: what an Oh My Pi launch resumes, `--resume=<file>`
	// (runtime/sandbox's agentArgv, the tmux runtime's launcher), the key the launcher finds the
	// session's row by in the session table (launcher's resumable and claudeKeeper), and the tmux
	// runtime's first look (sessionFileOf).
	HoldSessionFile HoldKind = "file"
	// HoldFileName is a session file's base name: where the recorded path is missing, the tmux
	// runtime resumes a file of that name one folder below a sessions directory (sessionFileOf).
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
// the file's path, its base name (filepath.Base, as the tmux runtime's search takes it), and the id
// its Oh My Pi name `<timestamp>_<id>.jsonl` gives; and the Claude Code session a file named
// `<uuid>.jsonl` gives, where that is not the session id itself. The last is wider than a Claude
// Code resume, which also asks for a `projects` directory two levels up (claudelaunch.Transcript):
// it refuses more, never less, and no Oh My Pi file is named for a bare uuid. An empty session or
// file names nothing. The claim_sessions migration's backfill spells the same rule in SQL, and its
// test holds the two to one answer.
func RecordedHolds(session, sessionFile string) []Hold {
	var holds []Hold
	if session != "" {
		holds = append(holds, Hold{Kind: HoldSessionID, Name: session})
	}
	if sessionFile == "" {
		return holds
	}
	base := filepath.Base(sessionFile)
	holds = append(holds, Hold{Kind: HoldSessionFile, Name: sessionFile}, Hold{Kind: HoldFileName, Name: base})
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
// rightful holder, and that claim's next decision on this same machine must see it. A store that
// does not answer refuses nothing and is asked again by the next decision.
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
	}); err != nil {
		return fmt.Errorf("hold the session of %s: %w", m.claim.Token, err)
	}
	if len(holders) != len(ask) {
		return fmt.Errorf("hold the session of %s: the store answered %d holders for %d names", m.claim.Token, len(holders), len(ask))
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
